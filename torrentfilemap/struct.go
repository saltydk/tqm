package torrentfilemap

import (
	"github.com/saltydk/tqm/config"
)

type TorrentFileMap struct {
	torrentFileMap map[string]map[string]config.Torrent
}
