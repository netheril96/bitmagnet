package model_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/database/fts"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestContent_UpdateTsv_Overwrite(t *testing.T) {
	c := model.Content{
		Title: "Hello 世界",
		Tsv:   fts.Tsvector{"hello": {}, "shi": {}, "jie": {}}, // Old incorrect TSV
	}
	c.UpdateTsv()
	assert.Contains(t, c.Tsv, "hello")
	assert.Contains(t, c.Tsv, "世")
	assert.Contains(t, c.Tsv, "界")
	assert.NotContains(t, c.Tsv, "shi")
	assert.NotContains(t, c.Tsv, "jie")
}

func TestTorrentContent_UpdateTsv_Overwrite(t *testing.T) {
	tc := model.TorrentContent{
		Torrent: model.Torrent{
			Name: "Hello 世界",
		},
		Tsv: fts.Tsvector{"hello": {}, "shi": {}, "jie": {}}, // Old incorrect TSV
	}
	tc.UpdateTsv()
	assert.Contains(t, tc.Tsv, "hello")
	assert.Contains(t, tc.Tsv, "世")
	assert.Contains(t, tc.Tsv, "界")
	assert.NotContains(t, tc.Tsv, "shi")
	assert.NotContains(t, tc.Tsv, "jie")
}
