package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

const (
	// CodeNotIncluded is a file nginx would never load. It is a refusal and not
	// a warning: writing a .conf that no include reaches produces a file the
	// author believes is live and the server has never read, which is worse
	// than writing nothing.
	CodeNotIncluded RefusalCode = "ops_not_included"

	// CodeFileExists is a path that is already there. Overwriting a file whose
	// contents nobody read is the one thing a create must never do.
	CodeFileExists RefusalCode = "ops_file_exists"
)

// CreateFile adds a .conf to the configuration.
//
// The check that matters is not whether the path is writable, it is whether
// nginx will LOAD it. A new file in a directory no include reaches is invisible
// to the server, and the author will believe their site is configured.
func CreateFile(tree *config.Tree, root, path, content string, mode os.FileMode) (*plan.Plan, error) {
	if path == "" {
		return nil, &Refusal{CodeInvalidArguments, "create was given no path"}
	}
	if !filepath.IsAbs(path) {
		return nil, &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"%s is not an absolute path. A relative path would resolve against wherever "+
				"ngx happens to be running, which is not where nginx reads from", path)}
	}
	if _, err := os.Stat(path); err == nil {
		return nil, &Refusal{CodeFileExists, fmt.Sprintf(
			"%s already exists. ngx will not overwrite a file whose contents it has not "+
				"read -- edit it, or remove it first", path)}
	} else if !os.IsNotExist(err) {
		return nil, &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"cannot tell whether %s exists: %v", path, err)}
	}

	pattern, ok := includeCovering(tree, path)
	if !ok {
		return nil, &Refusal{CodeNotIncluded, fmt.Sprintf(
			"nothing in this configuration includes %s, so nginx would never read it. "+
				"Add an `include` that covers it first, or put the file where an existing "+
				"one points", path)}
	}

	// The content has to be configuration nginx can read, and that is checked
	// here rather than left to apply: a create whose content does not parse
	// would be rolled back after being written, which is a worse way to learn
	// the same thing.
	if err := verifyFileContent(content); err != nil {
		return nil, err
	}

	return &plan.Plan{
		Root:       root,
		ConfigHash: tree.Hash,
		Creates: []plan.Create{{
			File:    path,
			Content: content,
			Mode:    fmt.Sprintf("%04o", mode.Perm()),
			Reason:  "create, covered by " + pattern,
		}},
	}, nil
}

// DeleteFile removes a .conf from the configuration.
//
// It reports what disappears with it, because a .conf is rarely one directive:
// removing a file with three server blocks takes three sites offline, and an
// operator who reads "deleted 1 file" has not been told that.
func DeleteFile(tree *config.Tree, root, path string) (*plan.Plan, error) {
	var target *config.File
	for _, f := range tree.Files {
		if f.Path == path {
			target = f
		}
	}
	if target == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s is not part of this configuration. Only a file nginx actually loads can be "+
				"removed from it -- a path that is merely on disk is not ngx's to delete",
			path)}
	}
	if path == root {
		return nil, &Refusal{CodeUnsupportedTarget, fmt.Sprintf(
			"%s is the top-level configuration file. Deleting it would leave nginx with "+
				"nothing to read", path)}
	}

	servers, locations := countBlocks(target.Nodes)

	return &plan.Plan{
		Root:       root,
		ConfigHash: tree.Hash,
		Deletes: []plan.Delete{{
			File: path,
			Reason: fmt.Sprintf("delete, taking %d server block(s) and %d location(s) with it",
				servers, locations),
		}},
	}, nil
}

// includeCovering answers whether some include in the configuration would pick
// the path up, and which one.
//
// It matches against the include's PATTERN rather than against the files that
// pattern currently resolves to, and that distinction is the whole function: a
// new file in conf.d/ is covered by `include conf.d/*.conf` even though it is
// not in the list of files that include produced when the configuration was
// read.
func includeCovering(tree *config.Tree, path string) (string, bool) {
	var found string
	for _, f := range tree.Files {
		walkNodes(f.Nodes, func(n *config.Node) {
			if found != "" || n.Directive != "include" || len(n.Args) == 0 {
				return
			}
			pattern := n.Args[0]
			if !filepath.IsAbs(pattern) {
				// Relative patterns resolve against the directory of the
				// top-level file, which is the approximation crossplane makes
				// too (combine.go records it).
				pattern = filepath.Join(filepath.Dir(tree.Files[0].Path), pattern)
			}
			if ok, err := filepath.Match(pattern, path); err == nil && ok {
				found = n.Args[0]
			}
		})
	}
	return found, found != ""
}

// verifyFileContent parses the content the way nginx will read it.
//
// A file is not a directive, so verifyHead cannot be reused: the content is a
// sequence of directives in whatever context the include sits in, and what is
// checked here is only that it LEXES and PARSES. Whether those directives are
// legal in that context is nginx's judgement, and apply asks it.
func verifyFileContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return &Refusal{CodeInvalidArguments,
			"the content is empty. An empty .conf is legal and does nothing, which is " +
				"almost certainly not what was meant -- if it is, create it with a comment in it"}
	}

	dir, err := os.MkdirTemp("", "ngx-ops-*")
	if err != nil {
		return &Refusal{CodeInvalidArguments, "could not check the content: " + err.Error()}
	}
	defer os.RemoveAll(dir)

	// Wrapped in the context a conf.d file usually sits in, because a bare
	// `server { }` does not parse on its own -- and refusing valid content for
	// that reason would be a defect in this check rather than in the content.
	probe := filepath.Join(dir, "probe.conf")
	wrapped := "events { worker_connections 1; }\nhttp {\n" + content + "\n}\n"
	if err := os.WriteFile(probe, []byte(wrapped), 0o600); err != nil {
		return &Refusal{CodeInvalidArguments, "could not check the content: " + err.Error()}
	}

	if _, err := config.Parse(config.ParseOptions{Path: probe}); err != nil {
		return &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"the content is not configuration ngx can read: %v", err)}
	}
	return nil
}

func countBlocks(nodes []*config.Node) (servers, locations int) {
	walkNodes(nodes, func(n *config.Node) {
		switch n.Directive {
		case "server":
			if n.HasBlock() {
				servers++
			}
		case "location":
			if n.HasBlock() {
				locations++
			}
		}
	})
	return servers, locations
}

func walkNodes(nodes []*config.Node, fn func(*config.Node)) {
	for _, n := range nodes {
		fn(n)
		walkNodes(n.Block, fn)
	}
}
