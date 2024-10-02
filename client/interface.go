package client

import (
	"github.com/saltydk/tqm/config"
)

type Interface interface {
	Type() string
	Connect() error
	GetTorrents() (map[string]config.Torrent, error)
	RemoveTorrent(string, bool) (bool, error)
	SetTorrentLabel(string, string) error
	GetCurrentFreeSpace(string) (uint64, error)
	AddFreeSpace(int64)
	GetFreeSpace() float64

	ShouldIgnore(*config.Torrent) (bool, error)
	ShouldRemove(*config.Torrent) (bool, error)
	ShouldRelabel(*config.Torrent) (string, bool, error)
}
