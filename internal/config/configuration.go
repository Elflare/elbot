package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type document struct {
	path    string
	data    []byte
	raw     map[string]any
	value   any
	err     error
	kind    string
	missing bool
}

type origin struct {
	field []string
	path  string
}

type configuration struct {
	cfg         *Config
	root        string
	assets      map[string]DefaultAsset
	definitions []Definition
	documents   map[string]*document
	paths       map[string]string
	pathErrors  map[string]error
	origins     []origin
}

func newConfiguration(path string, definitions []Definition) *configuration {
	core := CoreDefinitions()
	state := &configuration{
		cfg: defaultAppConfig(), root: core[0].Asset,
		assets: map[string]DefaultAsset{}, definitions: append(core, definitions...),
		documents: map[string]*document{}, paths: map[string]string{core[0].Asset: path},
		pathErrors: map[string]error{}, origins: []origin{{path: path}},
	}
	state.cfg.ConfigPath = path
	for _, asset := range DefaultAssets() {
		name := filepath.ToSlash(asset.Path)
		state.assets[name] = asset
		if len(asset.Reference) > 0 && asset.Topic != "" {
			state.definitions[0].Rules = append(state.definitions[0].Rules, Rule{Path: asset.Reference, Topic: asset.Topic})
		}
	}
	return state
}

func (s *configuration) definition(asset string) (Definition, bool) {
	for _, definition := range s.definitions {
		if definition.Asset == asset && len(definition.Node) == 0 {
			// Subtree rules also describe topics/examples in the root document.
			for _, child := range s.definitions {
				if child.Asset == asset && len(child.Node) > 0 {
					definition.Rules = append(definition.Rules, Rule{Path: child.Node, Topic: child.Topic, Children: child.Rules})
				}
			}
			return definition, true
		}
	}
	return Definition{Asset: asset}, false
}

func (s *configuration) assetPath(name string) (string, error) {
	if path, exists := s.paths[name]; exists {
		return path, s.pathErrors[name]
	}
	asset := s.assets[name]
	rootPath := s.paths[s.root]
	path := filepath.Join(filepath.Dir(rootPath), asset.Path)
	if len(asset.Reference) > 0 {
		main := s.documents[s.root]
		if main == nil || main.raw == nil {
			return "", fmt.Errorf("主配置无法读取或解析，未检查依赖 config_files 和 soul.path 定位的文件。")
		}
		fallback, _ := lookup(defaultAppConfig(), asset.Reference)
		value := fallback
		var current any = main.raw
		for _, key := range asset.Reference {
			table, ok := current.(map[string]any)
			if !ok {
				return "", fmt.Errorf("部分引用路径无效，未检查对应文件。")
			}
			child, exists := table[key]
			if !exists {
				current = fallback
				break
			}
			current = child
		}
		if current != nil {
			value = current
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("无法定位 %s，未检查该文件。", name)
		}
		if text == "" {
			text, _ = fallback.(string)
		}
		path = resolveRelative(rootPath, text)
	}
	s.paths[name] = path
	return path, nil
}

func readDocument(path string, newValue func() any) *document {
	doc := &document{path: path}
	doc.data, doc.err = os.ReadFile(path)
	if doc.err != nil {
		doc.missing = errors.Is(doc.err, os.ErrNotExist)
		doc.err = fmt.Errorf("read config %q: %w", path, doc.err)
		return doc
	}
	decodeDocument(doc, newValue)
	return doc
}

func decodeDocument(doc *document, newValue func() any) {
	path := doc.path
	if err := toml.Unmarshal(doc.data, &doc.raw); err != nil {
		doc.kind = "TOML 格式错误"
		doc.raw = nil // A partial parse must not supply dependent paths.
		doc.err = wrapDecodeError(path, err)
		return
	}
	if doc.raw == nil {
		doc.raw = map[string]any{}
	}
	if newValue != nil {
		value := newValue()
		if err := toml.Unmarshal(doc.data, value); err != nil {
			doc.kind = "字段类型错误"
			doc.err = wrapDecodeError(path, err)
			return
		}
		doc.value = value
	}
}

func wrapDecodeError(path string, err error) error {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		row, column := decodeErr.Position()
		return fmt.Errorf("parse config %q at line %d, column %d: %w", path, row, column, err)
	}
	return fmt.Errorf("parse config %q: %w", path, err)
}

// readCore is the common disk/default/merge path. Only startup stops at the
// first failed document; inspection preserves independent readable documents.
func (s *configuration) readCore(ctx context.Context, collect bool) error {
	for _, definition := range s.definitions {
		if definition.Apply == nil || len(definition.Node) > 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := s.assetPath(definition.Asset)
		if err != nil {
			s.pathErrors[definition.Asset] = err
			continue
		}
		doc := readDocument(path, definition.New)
		s.documents[definition.Asset] = doc
		if doc.err != nil {
			if !(doc.missing && s.assets[definition.Asset].Presence == Optional) && !collect {
				return doc.err
			}
		} else {
			for _, field := range definition.Apply(s.cfg, doc.value) {
				s.origins = append(s.origins, origin{field: field, path: path})
			}
		}
		if definition.BindPath != nil {
			definition.BindPath(s.cfg, path)
		}
	}
	if s.cfg.Session.DefaultMode == "" {
		s.cfg.Session.DefaultMode = "work"
	}
	if s.cfg.ModeModels == nil {
		s.cfg.ModeModels = map[string]ModelSelection{}
	}
	s.cfg.Storage.SessionsSQLitePath = resolveRelative(s.cfg.ConfigPath, s.cfg.Storage.SessionsSQLitePath)
	s.cfg.Storage.ChatHistorySQLitePath = resolveRelative(s.cfg.ConfigPath, s.cfg.Storage.ChatHistorySQLitePath)
	s.cfg.Soul.Path = resolveRelative(s.cfg.ConfigPath, s.cfg.Soul.Path)
	s.cfg.Sandbox.Root = resolveRelative(s.cfg.ConfigPath, s.cfg.Sandbox.Root)
	return nil
}
