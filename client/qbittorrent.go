package client

import (
	"fmt"
	"github.com/hashicorp/go-version"
	"github.com/shirou/gopsutil/disk"
	"github.com/sirupsen/logrus"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/saltydk/go-qbt"
	"github.com/saltydk/tqm/config"
	"github.com/saltydk/tqm/expression"
	"github.com/saltydk/tqm/logger"
	"github.com/saltydk/tqm/sliceutils"
)

/* Struct */

type QBittorrent struct {
	Url      *string `validate:"required"`
	User     string
	Password string

	// internal
	log        *logrus.Entry
	clientType string
	client     *qbittorrent.Client

	// set by cmd handler
	freeSpaceGB  float64
	freeSpaceSet bool

	// internal compiled filters
	exp *expression.Expressions
}

/* Initializer */

func NewQBittorrent(name string, exp *expression.Expressions) (Interface, error) {
	tc := QBittorrent{
		log:        logger.GetLogger(name),
		clientType: "qBittorrent",
		exp:        exp,
	}

	// load config
	if err := config.K.Unmarshal(fmt.Sprintf("clients%s%s", config.Delimiter, name), &tc); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// validate config
	if errs := config.ValidateStruct(tc); errs != nil {
		return nil, fmt.Errorf("validate config: %v", errs)
	}

	// init client
	qbl := logrus.New()
	qbl.Out = io.Discard
	tc.client = qbittorrent.NewClient(strings.TrimSuffix(*tc.Url, "/"), qbl)

	return &tc, nil
}

/* Interface  */

func (c *QBittorrent) Type() string {
	return c.clientType
}

func (c *QBittorrent) Connect() error {
	// login
	if err := c.client.Login(c.User, c.Password); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	// retrieve & validate api version
	apiVersionStr, err := c.client.Application.GetAPIVersion()
	if err != nil {
		return fmt.Errorf("get api version: %w", err)
	}

	c.log.Debugf("API Version: %v", apiVersionStr)

	// Parse version string
	apiVersion, err := version.NewVersion(apiVersionStr)
	if err != nil {
		return fmt.Errorf("parse api version: %w", err)
	}

	// Define minimum supported version
	minVersion, _ := version.NewVersion("2.2")

	if apiVersion.LessThan(minVersion) {
		return fmt.Errorf("unsupported webapi version: %v", apiVersionStr)
	}

	return nil
}

func (c *QBittorrent) GetTorrents() (map[string]config.Torrent, error) {
	// retrieve torrents from client
	c.log.Tracef("Retrieving torrents...")
	t, err := c.client.Torrent.GetList(nil)
	if err != nil {
		return nil, fmt.Errorf("get torrents: %w", err)
	}
	c.log.Tracef("Retrieved %d torrents", len(t))

	// build torrent list
	torrents := make(map[string]config.Torrent)
	for _, t := range t {
		t := t

		// get additional torrent details
		td, err := c.client.Torrent.GetProperties(t.Hash)
		if err != nil {
			return nil, fmt.Errorf("get torrent properties: %v: %w", t.Hash, err)
		}

		ts, err := c.client.Torrent.GetTrackers(t.Hash)
		if err != nil {
			return nil, fmt.Errorf("get torrent trackers: %v: %w", t.Hash, err)
		}

		tf, err := c.client.Torrent.GetContents(t.Hash)
		if err != nil {
			return nil, fmt.Errorf("get torrent files: %v: %w", t.Hash, err)
		}

		// parse tracker details
		trackerName := ""
		trackerStatus := ""

		for _, tracker := range ts {
			// skip disabled trackers
			if strings.Contains(tracker.URL, "[DHT]") || strings.Contains(tracker.URL, "[LSD]") ||
				strings.Contains(tracker.URL, "[PeX]") {
				continue
			}

			// use status of first enabled tracker
			trackerName = parseTrackerDomain(tracker.URL)
			trackerStatus = tracker.Message
			break
		}

		// added time
		addedTimeSecs := int64(time.Since(td.AdditionDate).Seconds())

		// torrent files
		var files []string
		for _, f := range tf {
			files = append(files, filepath.Join(td.SavePath, f.Name))
		}

		// create torrent
		torrent := config.Torrent{
			Hash:            t.Hash,
			Name:            t.Name,
			Path:            td.SavePath,
			TotalBytes:      int64(t.Size),
			DownloadedBytes: int64(td.TotalDownloaded),
			State:           string(t.State),
			Files:           files,
			Downloaded: !sliceutils.StringSliceContains([]string{
				"downloading",
				"stalledDL",
				"queuedDL",
				"pausedDL",
				"stoppedDL",
				"checkingDL",
			}, string(t.State), true),
			Seeding: sliceutils.StringSliceContains([]string{
				"uploading",
				"stalledUP",
			}, string(t.State), true),
			Ratio:          float32(td.ShareRatio),
			AddedSeconds:   addedTimeSecs,
			AddedHours:     float32(addedTimeSecs) / 60 / 60,
			AddedDays:      float32(addedTimeSecs) / 60 / 60 / 24,
			SeedingSeconds: int64(td.SeedingTime.Seconds()),
			SeedingHours:   float32(td.SeedingTime.Seconds()) / 60 / 60,
			SeedingDays:    float32(td.SeedingTime.Seconds()) / 60 / 60 / 24,
			Label:          t.Category,
			Seeds:          int64(td.SeedsTotal),
			Peers:          int64(td.PeersTotal),
			// free space
			FreeSpaceGB:  c.GetFreeSpace,
			FreeSpaceSet: c.freeSpaceSet,
			// tracker
			TrackerName:   trackerName,
			TrackerStatus: trackerStatus,
		}

		torrents[t.Hash] = torrent
	}

	return torrents, nil
}

func (c *QBittorrent) RemoveTorrent(hash string, deleteData bool) (bool, error) {
	// retrieve api version
	apiVersionStr, err := c.client.Application.GetAPIVersion()
	if err != nil {
		return false, fmt.Errorf("get api version: %w", err)
	}

	// Parse version string
	apiVersion, err := version.NewVersion(apiVersionStr)
	if err != nil {
		return false, fmt.Errorf("parse api version: %w", err)
	}

	// Define cutoff version
	cutoffVersion, _ := version.NewVersion("2.11")

	if apiVersion.LessThan(cutoffVersion) {
		// stop torrent (<5.0.0)
		if err := c.client.Torrent.PauseTorrents([]string{hash}); err != nil {
			return false, fmt.Errorf("stop torrent: %v: %w", hash, err)
		}
	} else {
		// stop torrent (5.0.0+)
		if err := c.client.Torrent.StopTorrents([]string{hash}); err != nil {
			return false, fmt.Errorf("stop torrent: %v: %w", hash, err)
		}
	}

	time.Sleep(1 * time.Second)

	if apiVersion.LessThan(cutoffVersion) {
		// Resume torrent (<5.0.0)
		if err := c.client.Torrent.ResumeTorrents([]string{hash}); err != nil {
			return false, fmt.Errorf("resume torrent: %v: %w", hash, err)
		}
	} else {
		// start torrent (5.0.0+)
		if err := c.client.Torrent.StartTorrents([]string{hash}); err != nil {
			return false, fmt.Errorf("start torrent: %v: %w", hash, err)
		}
	}

	// sleep before re-announcing torrent
	time.Sleep(2 * time.Second)

	if err := c.client.Torrent.ReannounceTorrents([]string{hash}); err != nil {
		return false, fmt.Errorf("re-announce torrent: %v: %w", hash, err)
	}

	// sleep before removing torrent
	time.Sleep(2 * time.Second)

	// remove torrent
	if err := c.client.Torrent.DeleteTorrents([]string{hash}, deleteData); err != nil {
		return false, fmt.Errorf("delete torrent: %v: %w", hash, err)
	}

	return true, nil
}

func (c *QBittorrent) SetTorrentLabel(hash string, label string) error {
	// set label
	if err := c.client.Torrent.SetCategories([]string{hash}, label); err != nil {
		return fmt.Errorf("set torrent label: %v: %w", label, err)
	}

	return nil
}

func (c *QBittorrent) GetCurrentFreeSpace(path string) (uint64, error) {
	// get current main stats
	data, err := disk.Usage(path)
	if err != nil {
		return 0, fmt.Errorf("get main data: %w", err)
	}

	// set internal free size
	c.freeSpaceGB = float64(data.Free) / humanize.GiByte
	c.freeSpaceSet = true

	return data.Free, nil
}

func (c *QBittorrent) AddFreeSpace(bytes int64) {
	c.freeSpaceGB += float64(bytes) / humanize.GiByte
}

func (c *QBittorrent) GetFreeSpace() float64 {
	return c.freeSpaceGB
}

/* Filters */

func (c *QBittorrent) ShouldIgnore(t *config.Torrent) (bool, error) {
	match, err := expression.CheckTorrentSingleMatch(t, c.exp.Ignores)
	if err != nil {
		return true, fmt.Errorf("check ignore expression: %v: %w", t.Hash, err)
	}

	return match, nil
}

func (c *QBittorrent) ShouldRemove(t *config.Torrent) (bool, error) {
	match, err := expression.CheckTorrentSingleMatch(t, c.exp.Removes)
	if err != nil {
		return false, fmt.Errorf("check remove expression: %v: %w", t.Hash, err)
	}

	return match, nil
}

func (c *QBittorrent) ShouldRelabel(t *config.Torrent) (string, bool, error) {
	for _, label := range c.exp.Labels {
		// check update
		match, err := expression.CheckTorrentAllMatch(t, label.Updates)
		if err != nil {
			return "", false, fmt.Errorf("check update expression: %v: %w", t.Hash, err)
		} else if !match {
			continue
		}

		// we should re-label
		return label.Name, true, nil
	}

	return "", false, nil
}
