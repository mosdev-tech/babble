package codegen

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Пути — соглашение, а не настройка: в конфиге их нет, иначе в каждом сервисе
// раскладка будет своя и ни один инструмент не заработает по умолчанию.
const (
	ContractDir   = "babble"
	ServiceSpec   = "babble/service.yaml"
	ClientsDir    = "babble/clients"
	GeneratedRoot = "internal/generated"
	ServiceOut    = "internal/generated/service"
	ClientsOut    = "internal/generated/clients"
	StubRoot      = "internal/api/handler"
)

// ModulePath читает Go-модуль из go.mod корня сервиса: флага -module нет, врать
// ему нечем.
func ModulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}
	return "", errors.New("go.mod: module directive not found")
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Generate — вся генерация одного сервиса. Работает в две фазы: сначала всё
// собирается и форматируется в памяти, и только если всё удалось —
// internal/generated сносится и пишется заново.
func Generate(root string) error {
	module, err := ModulePath(root)
	if err != nil {
		return err
	}

	// Сервисный контракт необязателен: BFF, воркеру или CLI нужен только
	// исходящий вызов, своих методов они не публикуют. Тогда генерируются
	// одни клиенты — сервер, DTO и стабы не появляются.
	specData, err := os.ReadFile(filepath.Join(root, ServiceSpec))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", ServiceSpec, err)
	}
	clientsOnly := err != nil

	var (
		spec      *Spec
		model     *Model
		dtoImport string
	)

	files := map[string]string{}

	if !clientsOnly {
		spec, err = ParseSpec(specData, ServiceSpec)
		if err != nil {
			return err
		}
		if errs := LintSpec(spec); len(errs) > 0 {
			return lintFailure(errs)
		}
		model, err = BuildModel(spec)
		if err != nil {
			return err
		}

		dtoImport = module + "/" + ServiceOut + "/dto"
		specSum := sum(specData)

		files[filepath.Join(ServiceOut, "sdk.go")] = RenderServerSDK(model, dtoImport, ServiceSpec, specSum)
		files[filepath.Join(ServiceOut, "dto", "dto.go")] = RenderDTO("dto", model, ServiceSpec, specSum)
		files[filepath.Join(ServiceOut, "dto", "const.go")] = RenderConst("dto", model, ServiceSpec, specSum)

		// Self-client: клиент к своему же сервису, нужен интеграционным тестам.
		selfPkg := SafeIdent(PackageName(spec.Service))
		if err := addClient(files, selfPkg, spec.Service, model, ServiceSpec, specSum); err != nil {
			return err
		}
	}

	clients, err := listClientSpecs(root)
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(clients) {
		relPath := clients[name]
		data, err := os.ReadFile(filepath.Join(root, relPath))
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}
		cspec, err := ParseSpec(data, relPath)
		if err != nil {
			return err
		}
		if errs := LintSpec(cspec); len(errs) > 0 {
			return lintFailure(errs)
		}
		if cspec.Service != name {
			return fmt.Errorf("%s: x-babble-service is %q but the file is named %q.yaml", relPath, cspec.Service, name)
		}
		if spec != nil && cspec.Service == spec.Service {
			return fmt.Errorf("%s: client for own service is generated automatically, remove this file", relPath)
		}
		cmodel, err := BuildModel(cspec)
		if err != nil {
			return err
		}
		pkg := SafeIdent(PackageName(name))
		if err := addClient(files, pkg, name, cmodel, relPath, sum(data)); err != nil {
			return err
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("nothing to generate: neither %s nor %s/*.yaml found", ServiceSpec, ClientsDir)
	}

	formatted, err := formatAll(files)
	if err != nil {
		return err
	}

	// Снос — только после того, как всё сгенерировано и отформатировано.
	for _, dir := range []string{ServiceOut, ClientsOut} {
		if err := os.RemoveAll(filepath.Join(root, dir)); err != nil {
			return fmt.Errorf("clean %s: %w", dir, err)
		}
	}
	for _, rel := range sortedKeys(formatted) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(full, formatted[rel], 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		fmt.Println("wrote", rel)
	}

	if clientsOnly {
		return nil
	}

	return writeStubs(root, model, dtoImport)
}

func addClient(files map[string]string, pkg, name string, m *Model, source, specSum string) error {
	dir := filepath.Join(ClientsOut, name)
	for _, f := range []string{"sdk.go", "dto.go", "const.go"} {
		if _, dup := files[filepath.Join(dir, f)]; dup {
			return fmt.Errorf("client %q is generated twice", name)
		}
	}
	files[filepath.Join(dir, "sdk.go")] = RenderClientSDK(pkg, m, source, specSum)
	files[filepath.Join(dir, "dto.go")] = RenderDTO(pkg, m, source, specSum)
	files[filepath.Join(dir, "const.go")] = RenderConst(pkg, m, source, specSum)
	return nil
}

// writeStubs скаффолдит бизнес-хендлеры. Пишет только отсутствующие файлы и
// никогда ничего не удаляет: здесь живёт рукописный код.
func writeStubs(root string, m *Model, dtoImport string) error {
	for _, method := range m.Methods {
		dir := filepath.Join(root, StubRoot, method.Pkg)
		file := filepath.Join(dir, "handler.go")
		if _, err := os.Stat(file); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", file, err)
		}
		src, err := format.Source([]byte(RenderStub(method, dtoImport)))
		if err != nil {
			return fmt.Errorf("format stub %s: %w", method.Pkg, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		if err := os.WriteFile(file, src, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", file, err)
		}
		fmt.Println("scaffolded", filepath.Join(StubRoot, method.Pkg, "handler.go"))
	}
	return nil
}

func formatAll(files map[string]string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for _, rel := range sortedKeys(files) {
		src, err := format.Source([]byte(files[rel]))
		if err != nil {
			// Неотформатированный вывод на диск не попадает: сломанный кодоген
			// обязан выглядеть сломанным.
			return nil, fmt.Errorf("generated %s does not parse: %w\n--- source ---\n%s", rel, err, files[rel])
		}
		out[rel] = src
	}
	return out, nil
}

func listClientSpecs(root string) (map[string]string, error) {
	dir := filepath.Join(root, ClientsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ClientsDir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		out[name] = path.Join(ClientsDir, e.Name())
	}
	return out, nil
}

func lintFailure(errs []LintError) error {
	var b strings.Builder
	b.WriteString("contract does not satisfy the rpc profile:\n")
	for _, e := range errs {
		b.WriteString("  " + e.Error() + "\n")
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------- клиентские контракты ----------
//
// babble/clients/<name>.yaml пишется руками: это контракт зависимости в том
// объёме, который сервису реально нужен. Кодоген его только читает — не тащит,
// не перезаписывает и не сверяет с оригиналом; следить за соответствием
// приходится самостоятельно.
