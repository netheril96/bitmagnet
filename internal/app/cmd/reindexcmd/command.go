package reindexcmd

import (
	"encoding/json"
	"os"

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

func (p Params) saveCursor(cursor *Cursor) {
	data, _ := json.Marshal(cursor)
	_ = os.WriteFile(cursorFile, data, 0644)
}

func (p Params) reindexTorrentContents(ctx *cli.Context, d *dao.Query, cursor *Cursor, batchSize int) error {
	p.Logger.Info("Reindexing torrent_contents...")
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
		p.Logger.Infof("Processed %d torrent_contents, last info_hash: %s", len(tcs), cursor.TorrentContentsInfoHash)
	}
	return nil
}

func (p Params) reindexContent(ctx *cli.Context, d *dao.Query, cursor *Cursor, batchSize int) error {
	p.Logger.Info("Reindexing content...")
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
		p.Logger.Infof("Processed %d content, last PK: %s/%s/%s", len(cs), cursor.ContentType, cursor.ContentSource, cursor.ContentID)
	}
	return nil
}
