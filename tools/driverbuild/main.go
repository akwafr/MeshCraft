// Command driverbuild runs the driver image pipeline once, by hand: clone,
// build, print the image id. The ops-side tool for "does this repository
// build?", and the harness the pipeline was first proven with.
//
//	go run ./tools/driverbuild -url https://… -subdir drivers/x -tag test/x:dev
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"meshcraft/internal/imagebuild"
)

func main() {
	var url = flag.String("url", "", "git repository URL")
	var subdir = flag.String("subdir", "", "driver subdirectory (empty = root)")
	var ref = flag.String("ref", "", "branch, tag or commit (empty = default)")
	var tag = flag.String("tag", "meshcraft/driver-test:dev", "image tag")
	var socket = flag.String("socket", "", "docker socket (default /var/run/docker.sock)")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "usage: driverbuild -url <git-url> [-subdir d] [-ref r] [-tag t]")
		os.Exit(2)
	}
	var svc = imagebuild.New(*socket)
	var res, err = svc.Build(context.Background(), *url, *subdir, *ref, *tag, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("commit %s\nimage  %s\n", res.GitCommit, res.ImageID)
}
