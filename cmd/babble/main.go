// babble — кодогенератор контрактов: OpenAPI (профиль rpc) → серверный SDK,
// стабы хендлеров и типизированные клиенты.
//
// Запускается из корня сервиса:
//
//	go run github.com/mosdev-tech/babble/cmd/babble gen
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mosdev-tech/babble/internal/codegen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "babble:", err)
		os.Exit(1)
	}
}

const usage = `usage: babble <command>

  gen     сгенерировать сервер, стабы и клиентов
  lint    проверить контракты по правилам профиля rpc
`

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	switch args[0] {
	case "gen":
		return codegen.Generate(root)
	case "lint":
		return lint(root)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func lint(root string) error {
	var failed bool
	var paths []string

	// Сервисный контракт необязателен: у клиентских проектов его нет.
	if _, err := os.Stat(filepath.Join(root, codegen.ServiceSpec)); err == nil {
		paths = append(paths, codegen.ServiceSpec)
	}

	entries, err := os.ReadDir(filepath.Join(root, codegen.ClientsDir))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
				paths = append(paths, filepath.Join(codegen.ClientsDir, e.Name()))
			}
		}
	}

	if len(paths) == 0 {
		return fmt.Errorf("nothing to lint: neither %s nor %s/*.yaml found", codegen.ServiceSpec, codegen.ClientsDir)
	}

	for _, rel := range paths {
		spec, err := codegen.LoadSpec(filepath.Join(root, rel))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
			continue
		}
		spec.Path = rel
		for _, e := range codegen.LintSpec(spec) {
			fmt.Fprintln(os.Stderr, e.Error())
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("lint failed")
	}
	fmt.Println("lint: ok")
	return nil
}
