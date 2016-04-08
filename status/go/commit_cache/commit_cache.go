package commit_cache

import (
	"encoding/gob"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/skia-dev/glog"

	"go.skia.org/infra/go/buildbot"
	"go.skia.org/infra/go/gitinfo"
	"go.skia.org/infra/go/timer"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/go/vcsinfo"
)

/*
	Utilities for caching commit data.
*/

func init() {
	gob.Register([]interface{}{})
	gob.Register(map[string]interface{}{})
}

// CommitCache is a struct used for caching commit data. Stores ALL commits in
// the repository.
type CommitCache struct {
	BranchHeads []*gitinfo.GitBranch
	cacheFile   string
	Commits     []*vcsinfo.LongCommit
	db          buildbot.DB
	mutex       sync.RWMutex
	repo        *gitinfo.GitInfo
	requestSize int
}

// fromFile attempts to load the CommitCache from the given file.
func fromFile(cacheFile string) (*CommitCache, error) {
	defer timer.New("commit_cache.fromFile()").Stop()
	c := CommitCache{}
	if _, err := os.Stat(cacheFile); err != nil {
		return nil, fmt.Errorf("Could not stat cache file: %v", err)
	}
	f, err := os.Open(cacheFile)
	if err != nil {
		return nil, fmt.Errorf("Failed to open cache file %s: %v", cacheFile, err)
	}
	defer util.Close(f)
	if err := gob.NewDecoder(f).Decode(&c); err != nil {
		return nil, fmt.Errorf("Failed to read cache file %s: %v", cacheFile, err)
	}
	return &c, nil
}

// toFile saves the CommitCache to a file.
func (c *CommitCache) toFile() error {
	defer timer.New("commit_cache.toFile()").Stop()
	f, err := os.Create(c.cacheFile)
	if err != nil {
		return err
	}
	defer util.Close(f)
	if err := gob.NewEncoder(f).Encode(c); err != nil {
		return err
	}
	return nil
}

// New creates and returns a new CommitCache which watches the given repo.
// The initial update will load ALL commits from the repository, so expect
// this to be slow.
func New(repo *gitinfo.GitInfo, cacheFile string, requestSize int, db buildbot.DB) (*CommitCache, error) {
	defer timer.New("commit_cache.New()").Stop()
	c, err := fromFile(cacheFile)
	if err != nil {
		glog.Warningf("Failed to read commit cache from file; starting from scratch. Error: %v", err)
		c = &CommitCache{}
	}
	c.cacheFile = cacheFile
	c.db = db
	c.repo = repo
	c.requestSize = requestSize

	// Update the cache.
	if err := c.update(); err != nil {
		return nil, err
	}

	// Update in a loop.
	go func() {
		for _ = range time.Tick(time.Minute) {
			if err := c.update(); err != nil {
				glog.Errorf("Failed to update commit cache: %v", err)
			}
		}
	}()
	return c, nil
}

// NumCommits returns the number of commits in the cache.
func (c *CommitCache) NumCommits() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return len(c.Commits)
}

// Get returns the commit at the given index.
func (c *CommitCache) Get(idx int) (*vcsinfo.LongCommit, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if idx < 0 || idx >= len(c.Commits) {
		return nil, fmt.Errorf("Index out of range: %d not in [%d, %d)", idx, 0, len(c.Commits))
	}
	return c.Commits[idx], nil
}

// Slice returns a slice of LongCommits from the cache.
func (c *CommitCache) Slice(startIdx, endIdx int) ([]*vcsinfo.LongCommit, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if startIdx < 0 || startIdx > endIdx || endIdx > len(c.Commits) {
		return nil, fmt.Errorf("Index out of range: (%d < 0 || %d > %d || %d > %d)", startIdx, startIdx, endIdx, endIdx, len(c.Commits))
	}
	return c.Commits[startIdx:endIdx], nil
}

// RevisionsInDateRange returns a slice of revisions that happen within the bounded time.
func (c *CommitCache) RevisionsInDateRange(start, end time.Time) ([]string, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if len(c.Commits) == 0 {
		glog.Warning("No commits yet in commit cache")
		return []string{}, nil
	}

	startIdx := sort.Search(len(c.Commits), func(i int) bool {
		return c.Commits[i].Timestamp.After(start)
	})
	endIdx := sort.Search(len(c.Commits), func(i int) bool {
		return c.Commits[i].Timestamp.After(end)
	})
	commits := c.Commits[startIdx:endIdx]

	r := make([]string, 0, len(commits))
	for _, commit := range commits {
		r = append(r, commit.Hash)
	}
	return r, nil
}

// update syncs the source code repository and loads any new commits.
func (c *CommitCache) update() (rv error) {
	defer timer.New("CommitCache.update()").Stop()
	glog.Info("Reloading commits.")
	if err := c.repo.Update(true, true); err != nil {
		return fmt.Errorf("Failed to update the repo: %v", err)
	}
	from := time.Time{}
	n := c.NumCommits()
	if n > 0 {
		last, err := c.Get(n - 1)
		if err != nil {
			return fmt.Errorf("Failed to get last commit: %v", err)
		}
		from = last.Timestamp
	}
	newCommitHashes := c.repo.From(from)
	glog.Infof("Processing %d new commits.", len(newCommitHashes))
	newCommits := make([]*vcsinfo.LongCommit, len(newCommitHashes))
	if len(newCommitHashes) > 0 {
		for i, h := range newCommitHashes {
			d, err := c.repo.Details(h, false)
			if err != nil {
				return fmt.Errorf("Failed to obtain commit details for %s: %v", h, err)
			}
			newCommits[i] = d
		}
	}
	branchHeads, err := c.repo.GetBranches()
	if err != nil {
		return fmt.Errorf("Failed to read branch information from the repo: %v", err)
	}

	allCommits := append(c.Commits, newCommits...)
	// Update the cached values all at once at at the end.
	glog.Infof("Updating the cache.")
	// Write the cache to disk *after* unlocking it.
	defer func() {
		rv = c.toFile()
	}()
	defer timer.New("  CommitCache locked").Stop()
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.BranchHeads = branchHeads
	c.Commits = allCommits
	glog.Infof("Finished updating the cache.")
	return nil
}

type CommitData struct {
	Comments    map[string][]*buildbot.CommitComment `json:"comments"`
	Commits     []*vcsinfo.LongCommit                `json:"commits"`
	BranchHeads []*gitinfo.GitBranch                 `json:"branch_heads"`
	StartIdx    int                                  `json:"startIdx"`
	EndIdx      int                                  `json:"endIdx"`
}

// getRange returns the given commit range along with the branch heads.
// Assumes that the caller holds a read lock.
func (c *CommitCache) getRange(startIdx, endIdx int) (*CommitData, error) {
	glog.Infof("CommitCache.RangeAsJson")
	commits, err := c.Slice(startIdx, endIdx)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(commits))
	for _, c := range commits {
		hashes = append(hashes, c.Hash)
	}

	comments, err := c.db.GetCommitsComments(hashes)
	if err != nil {
		return nil, err
	}

	data := &CommitData{
		Comments:    comments,
		Commits:     commits,
		BranchHeads: c.BranchHeads,
		StartIdx:    startIdx,
		EndIdx:      endIdx,
	}
	return data, nil
}

// GetLastN returns the last N commits along with the branch heads.
func (c *CommitCache) GetLastN(n int) (*CommitData, error) {
	end := c.NumCommits()
	start := end - n
	if start < 0 {
		start = 0
	}
	return c.getRange(start, end)
}
