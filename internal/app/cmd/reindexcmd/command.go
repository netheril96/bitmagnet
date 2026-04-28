package reindexcmd

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Params struct {
	fx.In
	Dao    lazy.Lazy[*dao.Query]
	Logger *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Command *cli.Command `group:"commands"`
}

const cursorFile = ".reindex_cursor.json"

type Cursor struct {
	TorrentContentsInfoHash protocol.ID `json:"torrent_contents_info_hash"`
	ContentType             string      `json:"content_type"`
	ContentSource           string      `json:"content_source"`
	ContentID               string      `json:"content_id"`
}

func New(p Params) (Result, error) {
	return Result{Command: &cli.Command{
		Name:  "reindex",
		Usage: "Reindex all search vectors (tsv columns) in the database",
		Flags: []cli.Flag{
			&cli.UintFlag{
				Name:  "batchSize",
				Value: 1000,
			},
		},
		Action: p.action,
	}}, nil
}

func (p Params) action(ctx *cli.Context) error {
	d, err := p.Dao.Get()
	if err != nil {
		return err
	}
	batchSize := int(ctx.Uint("batchSize"))
	if batchSize == 0 {
		batchSize = 1000
	}

	cursor := Cursor{}
	if data, err := os.ReadFile(cursorFile); err == nil {
		if err := json.Unmarshal(data, &cursor); err != nil {
			p.Logger.Warnf("failed to unmarshal cursor file: %s", err)
		}
	}

	if err := p.reindexTorrentContents(ctx, d, &cursor, batchSize); err != nil {
		return err
	}

	if err := p.reindexContent(ctx, d, &cursor, batchSize); err != nil {
		return err
	}

	_ = os.Remove(cursorFile)
	p.Logger.Info("Reindexing completed successfully")
	return nil
}

type progressBar struct {
	message   string
	total     int64
	current   int64
	initial   int64
	startTime time.Time
}

func (pb *progressBar) update(current int64) {
	pb.current = current
	elapsed := time.Since(pb.startTime)
	speed := 0.0
	if elapsed > 0 {
		speed = float64(pb.current-pb.initial) / elapsed.Seconds()
	}
	percent := 0.0
	if pb.total > 0 {
		percent = float64(pb.current) / float64(pb.total) * 100
	}
	if percent > 100 {
		percent = 100
	}
	eta := time.Duration(0)
	if speed > 0 && pb.total > pb.current {
		remaining := float64(pb.total - pb.current)
		eta = time.Duration(remaining/speed) * time.Second
	}

	barLen := 20
	filledLen := int(float64(barLen) * percent / 100)
	if filledLen > barLen {
		filledLen = barLen
	}
	bar := strings.Repeat("#", filledLen) + strings.Repeat("-", barLen-filledLen)

	fmt.Fprintf(os.Stderr, "\r%s [%s] %5.2f%% (%d/%d) [%.2f/s] ETA %s",
		pb.message, bar, percent, pb.current, pb.total, speed, eta.Round(time.Second))
}

func (p Params) saveCursor(cursor *Cursor) {
	data, _ := json.Marshal(cursor)
	_ = os.WriteFile(cursorFile, data, 0644)
}

func (p Params) reindexTorrentContents(ctx *cli.Context, d *dao.Query, cursor *Cursor, batchSize int) error {
	initial := int64(binary.BigEndian.Uint32(cursor.TorrentContentsInfoHash[:4]))
	pb := progressBar{
		message:   "torrent_contents",
		total:     math.MaxUint32,
		current:   initial,
		initial:   initial,
		startTime: time.Now(),
	}
	for {
		tcs, err := d.TorrentContent.WithContext(ctx.Context).
			Where(d.TorrentContent.InfoHash.Gt(cursor.TorrentContentsInfoHash)).
			Order(d.TorrentContent.InfoHash).
			Limit(batchSize).
			Find()
		if err != nil {
			return err
		}
		if len(tcs) == 0 {
			pb.update(pb.total)
			fmt.Fprintln(os.Stderr)
			break
		}
		for _, tc := range tcs {
			tc.UpdateTsv()
			if err := d.TorrentContent.WithContext(ctx.Context).Save(tc); err != nil {
				return err
			}
			cursor.TorrentContentsInfoHash = tc.InfoHash
		}
		p.saveCursor(cursor)
		pb.update(int64(binary.BigEndian.Uint32(cursor.TorrentContentsInfoHash[:4])))
	}
	return nil
}

func (p Params) reindexContent(ctx *cli.Context, d *dao.Query, cursor *Cursor, batchSize int) error {
	total, err := d.Content.WithContext(ctx.Context).Count()
	if err != nil {
		return err
	}
	remaining := total
	if cursor.ContentType != "" {
		q := d.Content.WithContext(ctx.Context)
		ct, _ := model.ParseContentType(cursor.ContentType)
		q = q.Where(
			d.Content.Type.Gt(ct.String()),
		).Or(
			d.Content.Type.Eq(ct.String()),
			d.Content.Source.Gt(cursor.ContentSource),
		).Or(
			d.Content.Type.Eq(ct.String()),
			d.Content.Source.Eq(cursor.ContentSource),
			d.Content.ID.Gt(cursor.ContentID),
		)
		r, err := q.Count()
		if err == nil {
			remaining = r
		}
	}
	initial := total - remaining
	pb := progressBar{
		message:   "content         ",
		total:     total,
		current:   initial,
		initial:   initial,
		startTime: time.Now(),
	}
	var processed int64 = initial
	for {
		// Composite cursor logic for (type, source, id)
		q := d.Content.WithContext(ctx.Context)
		if cursor.ContentType != "" {
			ct, _ := model.ParseContentType(cursor.ContentType)
			q = q.Where(
				d.Content.Type.Gt(ct.String()),
			).Or(
				d.Content.Type.Eq(ct.String()),
				d.Content.Source.Gt(cursor.ContentSource),
			).Or(
				d.Content.Type.Eq(ct.String()),
				d.Content.Source.Eq(cursor.ContentSource),
				d.Content.ID.Gt(cursor.ContentID),
			)
		}

		cs, err := q.Order(d.Content.Type, d.Content.Source, d.Content.ID).
			Limit(batchSize).
			Find()
		if err != nil {
			return err
		}
		if len(cs) == 0 {
			pb.update(pb.total)
			fmt.Fprintln(os.Stderr)
			break
		}
		for _, c := range cs {
			c.UpdateTsv()
			if err := d.Content.WithContext(ctx.Context).Save(c); err != nil {
				return err
			}
			cursor.ContentType = c.Type.String()
			cursor.ContentSource = c.Source
			cursor.ContentID = c.ID
		}
		p.saveCursor(cursor)
		processed += int64(len(cs))
		pb.update(processed)
	}
	return nil
}
