package main

import (
	"fmt"
	"io/fs"
	"log"
	"path"

	"github.com/johnsiilver/jsonfs"
)

func main() {
	dir := jsonfs.MustNewDir(
		"",
		jsonfs.MustNewFile("First Name", "John"),
		jsonfs.MustNewFile("Last Name", "Doak"),
		jsonfs.MustNewDir(
			"Identities",
			jsonfs.MustNewFile("EmployeeID", 10),
			jsonfs.MustNewFile("SSNumber", "999-99-9999"),
		),
	)
	fsys := jsonfs.NewMemFS(dir)

	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Fatal(err)
		}
		p = path.Clean(p)

		switch x := d.(type) {
		case jsonfs.File:
			fmt.Printf("%s:%s:%v\n", p, x.JSONType(), x.Any())
		case jsonfs.Directory:
			fmt.Printf("%s/\n", p)
		}
		return nil
	})
}
