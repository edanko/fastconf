package fastconf

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
	"github.com/edanko/fastconf/layers"
)

var configFilePatterns = []string{"*.yaml", "*.yml", "*.json", "*.toml"}

// buildFileLayers returns the file sources for a configuration path.
func buildFileLayers(configPath, subsystem, application, environment string) ([]layers.Layer, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		return nil, fmt.Errorf("fastconf: stat config path %q: %w", configPath, err)
	}
	if !info.IsDir() {
		return []layers.Layer{layers.NewFile(configPath)}, nil
	}

	rootFS := os.DirFS(configPath)
	sources := []layers.Layer{productFilesLayer(rootFS)}

	if subsystem != "" {
		sources = append(sources, scopedFilesLayer(
			rootFS,
			path.Join("subsystems", subsystem),
			[]string{sections.Subsystems, subsystem},
		))
		if application != "" {
			sources = append(sources, scopedFilesLayer(
				rootFS,
				path.Join("subsystems", subsystem, sections.Applications, application),
				[]string{sections.Subsystems, subsystem, sections.Applications, application},
			))
		}
	}

	sources = append(sources, tenantDirsLayer(rootFS, "tenants", "", []string{sections.Tenants}, nil))
	if subsystem != "" {
		sources = append(sources, tenantDirsLayer(
			rootFS,
			"tenants",
			path.Join(sections.Subsystems, subsystem),
			[]string{sections.Tenants},
			[]string{sections.Subsystems, subsystem},
		))
	}

	if environment != "" {
		environmentDir := path.Join(sections.Environments, environment)
		sources = append(sources, environmentFilesLayer(rootFS, environmentDir, environment, nil))
		if subsystem != "" {
			sources = append(sources, environmentFilesLayer(
				rootFS,
				path.Join(environmentDir, sections.Subsystems, subsystem),
				environment,
				[]string{sections.Subsystems, subsystem},
			))
			if application != "" {
				sources = append(sources, environmentFilesLayer(
					rootFS,
					path.Join(environmentDir, sections.Subsystems, subsystem, sections.Applications, application),
					environment,
					[]string{sections.Subsystems, subsystem, sections.Applications, application},
				))
			}
		}
		sources = append(sources, tenantDirsLayer(
			rootFS,
			path.Join(environmentDir, sections.Tenants),
			"",
			[]string{sections.Environments, environment, sections.Tenants},
			nil,
		))
		if subsystem != "" {
			sources = append(sources, tenantDirsLayer(
				rootFS,
				path.Join(environmentDir, sections.Tenants),
				path.Join(sections.Subsystems, subsystem),
				[]string{sections.Environments, environment, sections.Tenants},
				[]string{sections.Subsystems, subsystem},
			))
		}
	}
	return sources, nil
}

// productFilesLayer loads files from the configuration root.
func productFilesLayer(rootFS fs.FS) layers.Layer {
	return documentLayer(rootFS, "", func(document map[string]any) (map[string]any, error) {
		return doc.Normalize(document), nil
	})
}

// scopedFilesLayer loads files from a subsystem or application directory and
// places root values, tenant values and environment values in their scoped
// sections.
func scopedFilesLayer(rootFS fs.FS, dir string, suffix []string) layers.Layer {
	return documentLayer(rootFS, dir, func(document map[string]any) (map[string]any, error) {
		return remapScoped(document, suffix)
	})
}

// environmentFilesLayer loads files from an environment directory.
func environmentFilesLayer(rootFS fs.FS, dir string, environment string, suffix []string) layers.Layer {
	return documentLayer(rootFS, dir, func(document map[string]any) (map[string]any, error) {
		return remapEnvironment(document, environment, suffix)
	})
}

// tenantDirsLayer loads files from every tenant directory and merges them under
// prefix.<tenant><suffix>.
func tenantDirsLayer(rootFS fs.FS, tenantsDir, tenantSubdir string, prefix []string, suffix []string) layers.Layer {
	tenantsDir = path.Clean(tenantsDir)
	return layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		tenants, err := tenantDirectories(rootFS, tenantsDir)
		if err != nil {
			return nil, err
		}
		for _, tenantCode := range tenants {
			tenantRoot := path.Join(tenantsDir, tenantCode)
			if tenantSubdir != "" {
				tenantRoot = path.Join(tenantRoot, tenantSubdir)
			}
			tenantFS, err := fs.Sub(rootFS, tenantRoot)
			if err != nil {
				return nil, fmt.Errorf("fastconf: open tenant %q: %w", tenantCode, err)
			}
			document, err := readDocument(ctx, tenantFS)
			if err != nil {
				return nil, fmt.Errorf("fastconf: load tenant %q: %w", tenantCode, err)
			}
			root, ok := doc.Root(document)
			if !ok || len(root) == 0 {
				continue
			}
			target := make([]string, 0, len(prefix)+1+len(suffix))
			target = append(target, prefix...)
			target = append(target, tenantCode)
			target = append(target, suffix...)

			patch := map[string]any{sections.Root: map[string]any{}}
			if err := mergeAt(patch, target, root); err != nil {
				return nil, err
			}
			maps.Merge(doc.Normalize(values), patch)
		}
		return values, nil
	})
}

// documentLayer reads the files of one directory and merges the transformed
// document into the incoming values.
func documentLayer(rootFS fs.FS, dir string, transform func(map[string]any) (map[string]any, error)) layers.Layer {
	return layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		target := rootFS
		if dir != "" {
			sub, err := fs.Sub(rootFS, dir)
			if err != nil {
				return nil, fmt.Errorf("fastconf: open config directory %q: %w", dir, err)
			}
			target = sub
		}
		document, err := readDocument(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("fastconf: load config directory %q: %w", dir, err)
		}
		transformed, err := transform(document)
		if err != nil {
			return nil, err
		}
		merged := doc.Normalize(values)
		maps.Merge(merged, transformed)
		return merged, nil
	})
}

func readDocument(ctx context.Context, fsys fs.FS) (map[string]any, error) {
	source := layers.NewGlob(fsys, configFilePatterns...)
	document, err := source.Load(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	return doc.Normalize(document), nil
}

func tenantDirectories(rootFS fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(rootFS, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fastconf: read tenants directory %q: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// remapScoped moves a merged directory document into its scoped sections.
func remapScoped(document map[string]any, suffix []string) (map[string]any, error) {
	root, ok := doc.Root(document)
	if !ok {
		return map[string]any{sections.Root: map[string]any{}}, nil
	}
	working := maps.DeepCopyMap(root)
	tenants, _ := maps.ExtractMap(working, []string{sections.Tenants})
	environments, _ := maps.ExtractMap(working, []string{sections.Environments})

	outRoot := map[string]any{}
	if err := mergeAt(outRoot, suffix, working); err != nil {
		return nil, err
	}
	for tenantCode, tenantValues := range tenants {
		target := make([]string, 0, 2+len(suffix))
		target = append(target, sections.Tenants, tenantCode)
		target = append(target, suffix...)
		if err := mergeAt(outRoot, target, tenantValues); err != nil {
			return nil, err
		}
	}
	for environmentName, rawValues := range environments {
		environmentValues, ok := rawValues.(map[string]any)
		if !ok {
			continue
		}
		environmentValues = maps.DeepCopyMap(environmentValues)
		environmentTenants, _ := maps.ExtractMap(environmentValues, []string{sections.Tenants})

		target := make([]string, 0, 2+len(suffix))
		target = append(target, sections.Environments, environmentName)
		target = append(target, suffix...)
		if err := mergeAt(outRoot, target, environmentValues); err != nil {
			return nil, err
		}
		for tenantCode, tenantValues := range environmentTenants {
			tenantTarget := make([]string, 0, 4+len(suffix))
			tenantTarget = append(tenantTarget, sections.Environments, environmentName, sections.Tenants, tenantCode)
			tenantTarget = append(tenantTarget, suffix...)
			if err := mergeAt(outRoot, tenantTarget, tenantValues); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{sections.Root: outRoot}, nil
}

// remapEnvironment moves a merged environment directory document under
// environments.<environment>.
func remapEnvironment(document map[string]any, environment string, suffix []string) (map[string]any, error) {
	root, ok := doc.Root(document)
	if !ok {
		return map[string]any{sections.Root: map[string]any{}}, nil
	}
	working := maps.DeepCopyMap(root)
	environmentTenants, _ := maps.ExtractMap(working, []string{sections.Tenants})

	outRoot := map[string]any{}
	target := make([]string, 0, 2+len(suffix))
	target = append(target, sections.Environments, environment)
	target = append(target, suffix...)
	if err := mergeAt(outRoot, target, working); err != nil {
		return nil, err
	}
	for tenantCode, tenantValues := range environmentTenants {
		tenantTarget := make([]string, 0, 4+len(suffix))
		tenantTarget = append(tenantTarget, sections.Environments, environment, sections.Tenants, tenantCode)
		tenantTarget = append(tenantTarget, suffix...)
		if err := mergeAt(outRoot, tenantTarget, tenantValues); err != nil {
			return nil, err
		}
	}
	return map[string]any{sections.Root: outRoot}, nil
}

// mergeAt deep-merges value at path in destination, creating intermediate
// objects.
func mergeAt(destination map[string]any, path []string, value any) error {
	valueMap, isMap := value.(map[string]any)
	if len(path) == 0 {
		if isMap {
			maps.Merge(destination, valueMap)
		}
		return nil
	}
	existing, ok := maps.GetMap(destination, path)
	if !ok || !isMap {
		return maps.Set(destination, path, maps.DeepCopy(value))
	}
	maps.Merge(existing, valueMap)
	return nil
}

// bootstrapFilePath returns the bootstrap document path.
func bootstrapFilePath(configPath, bootstrapPath string) string {
	if bootstrapPath != "" {
		return bootstrapPath
	}
	if configPath == "" {
		return "bootstrap.yml"
	}
	info, err := os.Stat(configPath)
	if err == nil && info.IsDir() {
		return filepath.Join(configPath, "bootstrap.yml")
	}
	return filepath.Join(filepath.Dir(configPath), "bootstrap.yml")
}
