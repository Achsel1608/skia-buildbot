package server

/*
   Utilities for creating GCE instances to run servers.
*/

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"

	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/gce"
	"go.skia.org/infra/go/sklog"
)

const (
	DISK_IMAGE_SKIA_PUSHABLE_BASE = "skia-pushable-base-v2017-06-13-003"
	GS_URL_GITCOOKIES_TMPL        = "gs://skia-buildbots/artifacts/server/.gitcookies_%s"
	GS_URL_GITCONFIG              = "gs://skia-buildbots/artifacts/server/.gitconfig"
	GS_URL_NETRC                  = "gs://skia-buildbots/artifacts/server/.netrc"
)

var (
	// Flags.
	instance       = flag.String("instance", "", "Which instance to create/delete.")
	create         = flag.Bool("create", false, "Create the instance. Either --create or --delete is required.")
	delete         = flag.Bool("delete", false, "Delete the instance. Either --create or --delete is required.")
	deleteDataDisk = flag.Bool("delete-data-disk", false, "Delete the data disk. Only valid with --delete")
	ignoreExists   = flag.Bool("ignore-exists", false, "Do not fail out when creating a resource which already exists or deleting a resource which does not exist.")
	workdir        = flag.String("workdir", ".", "Working directory.")
)

// Base config for server instances.
func Server20170518(name string) *gce.Instance {
	return &gce.Instance{
		BootDisk: &gce.Disk{
			Name:           name,
			SourceSnapshot: gce.DISK_SNAPSHOT_SYSTEMD_PUSHABLE_BASE,
			Type:           gce.DISK_TYPE_PERSISTENT_STANDARD,
		},
		DataDisk: &gce.Disk{
			Name:   fmt.Sprintf("%s-data", name),
			SizeGb: 300,
			Type:   gce.DISK_TYPE_PERSISTENT_STANDARD,
		},
		GSDownloads:       map[string]string{},
		MachineType:       gce.MACHINE_TYPE_HIGHMEM_16,
		Metadata:          map[string]string{},
		MetadataDownloads: map[string]string{},
		Name:              name,
		Os:                gce.OS_LINUX,
		Scopes: []string{
			auth.SCOPE_FULL_CONTROL,
			auth.SCOPE_PUBSUB,
			auth.SCOPE_USERINFO_EMAIL,
			auth.SCOPE_USERINFO_PROFILE,
		},
		Tags: []string{"http-server", "https-server"},
		User: gce.USER_DEFAULT,
	}
}

// Updated server config which uses the new disk image.
func Server20170613(name string) *gce.Instance {
	vm := Server20170518(name)
	vm.BootDisk.SourceImage = DISK_IMAGE_SKIA_PUSHABLE_BASE
	vm.BootDisk.SourceSnapshot = ""
	return vm
}

// Add configuration for servers who use git.
func AddGitConfigs(vm *gce.Instance, gitUser string) *gce.Instance {
	vm.GSDownloads["~/.gitconfig"] = GS_URL_GITCONFIG
	vm.GSDownloads["~/.netrc"] = GS_URL_NETRC
	vm.GSDownloads["~/.gitcookies"] = fmt.Sprintf(GS_URL_GITCOOKIES_TMPL, gitUser)
	return vm
}

// Main takes a map of string -> gce.Instance to initialize in the given zone.
// The string keys are nicknames for the instances (e.g. "prod", "staging").
//  Only the instance specified by the --instance flag will be created.
func Main(zone string, instances map[string]*gce.Instance) {
	common.Init()
	defer common.LogPanic()

	vm, ok := instances[*instance]
	if !ok {
		validInstances := make([]string, 0, len(instances))
		for k := range instances {
			validInstances = append(validInstances, k)
		}
		sort.Strings(validInstances)
		sklog.Fatalf("Invalid --instance %q; name must be one of: %v", *instance, validInstances)
	}
	if *create == *delete {
		sklog.Fatal("Please specify --create or --delete, but not both.")
	}

	// Get the absolute workdir.
	wdAbs, err := filepath.Abs(*workdir)
	if err != nil {
		sklog.Fatal(err)
	}

	// Create the GCloud object.
	g, err := gce.NewGCloud(zone, wdAbs)
	if err != nil {
		sklog.Fatal(err)
	}

	// Perform the requested operation.
	if *create {
		if err := g.CreateAndSetup(vm, *ignoreExists); err != nil {
			sklog.Fatal(err)
		}
	} else {
		if err := g.Delete(vm, *ignoreExists, *deleteDataDisk); err != nil {
			sklog.Fatal(err)
		}
	}
}
