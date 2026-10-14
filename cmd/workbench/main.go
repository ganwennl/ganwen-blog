package main

import (
	"context"
	"flag"
	"fmt"
	"ganwen-blog/internal/blog"
	"log"
	"path/filepath"
)

func main() {
	data := flag.String("data", "data", "data directory")
	path := flag.String("file", "migration/translation-tasks.json", "input/output JSON")
	apply := flag.Bool("apply", false, "import translated slots")
	publish := flag.Bool("publish", false, "publish translations in preview database")
	all := flag.Bool("include-private", false, "include draft and private original articles")
	replaceMachine := flag.Bool("replace-generated", false, "replace only unchanged machine-generated translations")
	flag.Parse()
	s, err := blog.OpenStore(filepath.Join(*data, "blog.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if *apply {
		n, err := blog.ImportOfflineTasksWithOptions(context.Background(), s, *path, *publish, *replaceMachine)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Applied %d validated translations.\n", n)
	} else {
		if *all {
			err = blog.ExportAllOfflineTasks(context.Background(), s, *path)
		} else {
			err = blog.ExportOfflineTasks(context.Background(), s, *path)
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("Exported translation slots.")
	}
}
