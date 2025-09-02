package server

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/responder"
	"go.uber.org/zap"
)

type Server interface {
	start() error
	stop()
	Query(ctx context.Context, addr netip.AddrPort, q string, args dht.MsgArgs) (dht.RecvMsg, error)
}

type MultiplexServer struct {
	servers        []*server
	startedServers []*server
	mutex          sync.RWMutex
}

type server struct {
	stopped          chan struct{}
	mutex            sync.Mutex
	localAddr        netip.AddrPort
	socket           Socket
	queryTimeout     time.Duration
	queries          map[string]chan dht.RecvMsg
	responder        responder.Responder
	responderTimeout time.Duration
	idIssuer         IDIssuer
	logger           *zap.SugaredLogger
}

func (s *server) start() error {
	if err := s.socket.Open(s.localAddr); err != nil {
		return fmt.Errorf("could not open socket: %w", err)
	}

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go s.read(ctx)
		<-s.stopped
		cancel()

		_ = s.socket.Close()
	}()

	return nil
}

func (s *MultiplexServer) start() error {
	var wg sync.WaitGroup
	startedCh := make(chan *server, len(s.servers))
	errCh := make(chan error, len(s.servers))

	for _, srv := range s.servers {
		wg.Add(1)
		go func(srv *server) {
			defer wg.Done()
			if err := srv.start(); err != nil {
				srv.logger.Errorw("could not start server", "addr", srv.localAddr, "error", err)
				errCh <- err
			} else {
				srv.logger.Infow("server started successfully", "addr", srv.localAddr)
				startedCh <- srv
			}
		}(srv)
	}

	wg.Wait()
	close(startedCh)
	close(errCh)

	s.mutex.Lock()
	for srv := range startedCh {
		s.startedServers = append(s.startedServers, srv)
	}
	s.mutex.Unlock()

	if len(s.startedServers) > 0 {
		return nil
	}

	if len(s.servers) == 0 {
		return errors.New("no servers configured")
	}

	var allErrors []error
	for err := range errCh {
		allErrors = append(allErrors, err)
	}
	return fmt.Errorf("could not start any server: %w", errors.Join(allErrors...))
}

func (s *MultiplexServer) stop() {
	for _, srv := range s.startedServers {
		srv.logger.Infow("stopping server", "addr", srv.localAddr)
		srv.stop()
	}
	s.startedServers = make([]*server, 0)
}

func (s *MultiplexServer) Query(
	ctx context.Context,
	addr netip.AddrPort,
	q string,
	args dht.MsgArgs,
) (dht.RecvMsg, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	var candidates []*server
	isIPv4 := addr.Addr().Is4()
	for _, srv := range s.startedServers {
		if srv.localAddr.Addr().Is4() == isIPv4 {
			candidates = append(candidates, srv)
		}
	}

	if !isIPv4 && !addr.Addr().Is6() {
		return dht.RecvMsg{}, errors.New("address is not IPv4 or IPv6")
	}

	if len(candidates) == 0 {
		family := "IPv6"
		if isIPv4 {
			family = "IPv4"
		}
		return dht.RecvMsg{}, fmt.Errorf("%s server not available", family)
	}

	var errs []error
	for _, srv := range candidates {
		res, err := srv.Query(ctx, addr, q, args)
		if err != nil {
			srv.logger.Debugw("query failed on server, trying next", "query", q, "addr", addr, "local_addr", srv.localAddr, "error", err)
			errs = append(errs, err)
			continue
		}
		srv.logger.Debugw("query succeeded on server", "query", q, "addr", addr, "local_addr", srv.localAddr)
		return res, nil
	}

	return dht.RecvMsg{}, fmt.Errorf("all queries failed for %s: %w", addr.String(), errors.Join(errs...))
}

func (s *server) stop() {
	close(s.stopped)
}

func (s *server) read(ctx context.Context) {
	/*   The field size sets a theoretical limit of 65,535 bytes (8 byte header + 65,527 bytes of
	 * data) for a UDP datagram. However the actual limit for the data length, which is imposed by
	 * the underlying IPv4 protocol, is 65,507 bytes (65,535 − 8 byte UDP header − 20 byte IP
	 * header).
	 *
	 *   In IPv6 jumbograms it is possible to have UDP packets of size greater than 65,535 bytes.
	 * RFC 2675 specifies that the length field is set to zero if the length of the UDP header plus
	 * UDP data is greater than 65,535.
	 *
	 * https://en.wikipedia.org/wiki/User_Datagram_Protocol
	 */
	buffer := make([]byte, 65507)

	for {
		if ctx.Err() != nil {
			return
		}

		n, from, err := s.socket.Receive(buffer)
		if err != nil {
			// Socket is probably closed; if we're not shutting down then panic
			if ctx.Err() == nil {
				panic(fmt.Errorf("socket read error: %w", err))
			}

			return
		}

		if n == 0 {
			/* Datagram sockets in various domains  (e.g., the UNIX and Internet domains) permit
			 * zero-length datagrams. When such a datagram is received, the return value (n) is 0.
			 */
			continue
		}

		var msg dht.Msg

		err = bencode.Unmarshal(buffer[:n], &msg)
		if err != nil {
			s.logger.Debugw("could not unmarshal packet data", "error", err)
			continue
		}

		recvMsg := dht.RecvMsg{
			Msg:  msg,
			From: from,
		}

		switch msg.Y {
		case dht.YQuery:
			go s.handleQuery(ctx, recvMsg)
		case dht.YResponse, dht.YError:
			go s.handleResponse(recvMsg)
		}
	}
}

func (s *server) handleQuery(ctx context.Context, msg dht.RecvMsg) {
	ctx, cancel := context.WithTimeout(ctx, s.responderTimeout)
	defer cancel()

	res := dht.Msg{
		T: msg.Msg.T,
		Y: dht.YResponse,
	}

	ret, retErr := s.responder.Respond(ctx, msg)
	if retErr != nil {
		dhtErr := &dht.Error{}
		if ok := errors.As(retErr, dhtErr); ok {
			res.E = dhtErr
		} else {
			res.E = &dht.Error{
				Code: dht.ErrorCodeServerError,
				Msg:  "server error",
			}

			s.logger.Errorw("server error", "msg", msg, "retErr", retErr)
		}
	} else {
		res.R = &ret
	}

	if sendErr := s.send(msg.From, res); sendErr != nil {
		s.logger.Debugw("could not send response", "msg", msg, "retErr", sendErr)
	}
}

func (s *server) handleResponse(msg dht.RecvMsg) {
	transactionID := msg.Msg.T

	s.mutex.Lock()
	ch, ok := s.queries[transactionID]
	s.mutex.Unlock()

	if ok {
		ch <- msg
	}
}

func (s *server) Query(
	ctx context.Context,
	addr netip.AddrPort,
	q string,
	args dht.MsgArgs,
) (r dht.RecvMsg, err error) {
	transactionID := s.idIssuer.Issue()
	ch := make(chan dht.RecvMsg, 1)

	s.mutex.Lock()
	s.queries[transactionID] = ch
	s.mutex.Unlock()

	defer (func() {
		s.mutex.Lock()
		delete(s.queries, transactionID)
		s.mutex.Unlock()
	})()

	msg := dht.Msg{
		Q: q,
		T: transactionID,
		A: &args,
		Y: dht.YQuery,
	}
	if sendErr := s.send(addr, msg); sendErr != nil {
		s.logger.Debugw("could not send query", "msg", msg, "sendErr", sendErr)
		err = sendErr

		return
	}

	queryCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	select {
	case <-queryCtx.Done():
		err = queryCtx.Err()
		return
	case res, ok := <-ch:
		if !ok {
			err = errors.New("channel closed")
			return
		}

		r = res

		if res.Msg.Y == dht.YError {
			err = res.Msg.E
			if err == nil {
				err = errors.New("error missing from response")
			}
		} else if r.Msg.R == nil {
			err = errors.New("return data missing from response")
		}

		return
	}
}

func (s *server) send(addr netip.AddrPort, msg dht.Msg) error {
	data, encodeErr := bencode.Marshal(msg)
	if encodeErr != nil {
		return encodeErr
	}

	sendErr := s.socket.Send(addr, data)
	if sendErr != nil {
		return sendErr
	}

	return nil
}
