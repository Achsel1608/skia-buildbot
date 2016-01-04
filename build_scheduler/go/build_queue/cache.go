package build_queue

import (
	"fmt"

	"go.skia.org/infra/go/buildbot_deprecated"
)

// buildCache is a struct used as an intermediary between the buildbot
// ingestion code and the database (see the BuildCache interface in
// go.skia.org/infra/go/buildbot package). It allows the BuildQueue to
// pretend to insert builds so that it can select the best build
// candidate at every step.
type buildCache struct {
	buildsByCommit map[string]*buildbot_deprecated.Build
	buildsByNumber map[int]*buildbot_deprecated.Build
	Builder        string
	Master         string
	MaxBuildNum    int
	Repo           string
}

// GetBuildForCommit returns the build number of the build which included the
// given commit, or -1 if no such build exists. It is used by the buildbot
// package's FindCommitsForBuild function.
func (bc *buildCache) GetBuildForCommit(builder, master, hash string) (int, error) {
	if b, ok := bc.buildsByCommit[hash]; ok {
		return b.Number, nil
	}
	// Fall back on getting the build from the database.
	b, err := buildbot_deprecated.GetBuildForCommit(builder, master, hash)
	if err != nil {
		return -1, fmt.Errorf("Failed to get build for %s at %s: %v", builder, hash[0:7], err)
	}
	return b, nil
}

// getBuildForCommit returns a buildbot.Build instance for the build which
// included the given commit, or nil if no such build exists.
func (bc *buildCache) getBuildForCommit(hash string) (*buildbot_deprecated.Build, error) {
	num, err := bc.GetBuildForCommit(bc.Builder, bc.Master, hash)
	if err != nil {
		return nil, err
	}
	b, err := bc.getByNumber(num)
	if err != nil {
		return nil, fmt.Errorf("Failed to get build for %s at %s: %v", bc.Builder, hash[0:7], err)
	}
	return b, nil
}

// getByNumber returns a buildbot.Build instance for the build with the
// given number.
func (bc *buildCache) getByNumber(number int) (*buildbot_deprecated.Build, error) {
	b, ok := bc.buildsByNumber[number]
	if !ok {
		b, err := buildbot_deprecated.GetBuildFromDB(bc.Builder, bc.Master, number)
		if err != nil {
			return nil, err
		}
		if b != nil {
			if err := bc.Put(b); err != nil {
				return nil, err
			}
		}
		return b, nil
	}
	return b, nil
}

// GetBuildFromDB returns the given Build.
func (bc *buildCache) GetBuildFromDB(builder, master string, number int) (*buildbot_deprecated.Build, error) {
	return bc.getByNumber(number)
}

// Put inserts the given build into the buildCache so that it will be found
// when any of the getter functions are called. It does not insert the build
// into the database.
func (bc *buildCache) Put(b *buildbot_deprecated.Build) error {
	build := &(*b) // Copy the build.
	for _, c := range b.Commits {
		bc.buildsByCommit[c] = build
	}
	bc.buildsByNumber[b.Number] = build
	if build.Number > bc.MaxBuildNum {
		bc.MaxBuildNum = build.Number
	}
	return nil
}

// PutMulti inserts all of the given builds into the buildCache.
func (bc *buildCache) PutMulti(builds []*buildbot_deprecated.Build) error {
	for _, b := range builds {
		if err := bc.Put(b); err != nil {
			return err
		}
	}
	return nil
}

// newBuildCache returns a buildCache instance for the given
// builder/master/repo combination.
func newBuildCache(builder, master, repo string) (*buildCache, error) {
	maxBuild, err := buildbot_deprecated.GetMaxBuildNumber(builder)
	if err != nil {
		return nil, err
	}
	return &buildCache{
		buildsByCommit: map[string]*buildbot_deprecated.Build{},
		buildsByNumber: map[int]*buildbot_deprecated.Build{},
		Builder:        builder,
		Master:         master,
		MaxBuildNum:    maxBuild,
		Repo:           repo,
	}, nil
}
