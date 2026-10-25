// 持有 Skill 内部依赖，协调扫描、注册替换、目录更新与文件变更。
package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"elbot/internal/fileops"
	"elbot/internal/processenv"
	toolmedia "elbot/internal/tool/media"
	toolregistry "elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

type Manager struct {
	root       string                  // 根部装配传入的技能资产目录。
	catalog    *catalog                // 当前已成功注册的技能记录。
	scanner    filesystemScanner       // 只读取技能文件并生成候选记录。
	registry   *toolregistry.Registry  // 借用 Tool 注册目录，仅替换 Skill 来源的工具。
	media      *toolmedia.MediaRuntime // 借用 Tool 媒体入口，交给可执行技能使用。
	processEnv processenv.Environment  // 启动时的基础环境，技能执行时再叠加自身 .env。

	writeMu sync.Mutex    // 串行化技能写入与重载；等待用户确认时不持有。
	mu      sync.Mutex    // 保护加载状态及等待通知。
	loaded  bool          // 最近一次加载结果是否成功，可直接复用活动目录。
	loading bool          // 是否已有 EnsureLoaded 正在加载。
	done    chan struct{} // 当前 EnsureLoaded 加载结束通知。
	lastErr error         // 最近一次加载或管理操作的错误。
}

// NewManager 接收根部已确定的目录与完整依赖，不自行选择默认配置目录或启动加载。
func NewManager(root string, registry *toolregistry.Registry, media *toolmedia.MediaRuntime, environment processenv.Environment) *Manager {
	return &Manager{
		root:       root,
		catalog:    newCatalog(),
		scanner:    newFilesystemScanner(root),
		registry:   registry,
		media:      media,
		processEnv: environment,
	}
}

func (m *Manager) Reload(ctx context.Context) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	err := m.reloadLocked(ctx)
	m.setReloadResult(err)
	return err
}

// reloadLocked 在持有 writeMu 时提交候选；注册失败不会替换活动目录。
func (m *Manager) reloadLocked(ctx context.Context) error {
	records, err := m.scanner.scanRecords(ctx)
	if err != nil {
		return err
	}
	replacements := make([]toolshare.Tool, 0, len(records))
	for _, record := range records {
		replacements = append(replacements, m.toolForRecord(record))
	}
	if err := m.registry.ReplaceSources([]toolshare.Source{toolshare.SourceSkillAgent, toolshare.SourceSkillGo}, replacements); err != nil {
		return err
	}
	m.catalog.Replace(records)
	return nil
}

func (m *Manager) toolForRecord(record record) toolshare.Tool {
	if record.Kind == KindAgent && record.ManifestFound && record.ManifestError == "" && record.Manifest.Callable {
		command := NewCommandTool(record)
		command.Media = m.media
		command.ProcessEnv = m.processEnv
		return command
	}
	return NewDescriptor(record)
}

func (m *Manager) setReloadResult(err error) {
	m.mu.Lock()
	m.loaded = err == nil
	m.lastErr = err
	m.mu.Unlock()
}

func (m *Manager) EnsureLoaded(ctx context.Context) error {
	m.mu.Lock()
	if m.loaded {
		err := m.lastErr
		m.mu.Unlock()
		return err
	}
	if m.loading {
		done := m.done
		m.mu.Unlock()
		select {
		case <-done:
			m.mu.Lock()
			err := m.lastErr
			m.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.loading = true
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()

	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	err := m.reloadLocked(ctx)

	m.mu.Lock()
	m.loaded = err == nil
	m.loading = false
	m.lastErr = err
	close(done)
	m.mu.Unlock()
	return err
}

func (m *Manager) StartDelayedReload(ctx context.Context, delay time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		_ = m.EnsureLoaded(ctx)
	}()
	return done
}

func (m *Manager) Remove(ctx context.Context, name string) (err error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	defer func() { m.setReloadResult(err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := m.catalog.Get(name)
	if !ok {
		return fmt.Errorf("external skill %q not found", name)
	}
	if record.Root == "" {
		return fmt.Errorf("external skill %q has no root directory", name)
	}
	if err := os.RemoveAll(record.Root); err != nil {
		return fmt.Errorf("remove skill directory %q: %w", record.Root, err)
	}
	return m.reloadLocked(ctx)
}

// WriteAgentSkillConfig commits an AgentSkill manifest and rolls the file back
// if the complete skill snapshot cannot be reloaded.
func (m *Manager) WriteAgentSkillConfig(ctx context.Context, name, content string) error {
	name = strings.TrimSpace(name)
	if err := validateSkillName(name); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("toml is required for write")
	}
	if _, err := ParseAgentSkillManifest([]byte(content)); err != nil {
		return err
	}

	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := m.catalog.Get(name)
	if !ok || record.Kind != KindAgent {
		return fmt.Errorf("AgentSkill %q not found", name)
	}
	path := AgentSkillConfigPath(record.Root)
	previous, err := captureFile(path)
	if err != nil {
		return err
	}
	if err := fileops.AtomicWriteFile(path, []byte(content), existingFileMode(path)); err != nil {
		return err
	}
	if err := m.reloadLocked(ctx); err != nil {
		if rollbackErr := previous.restore(path); rollbackErr != nil {
			combined := fmt.Errorf("reload and restore previous %s failed: %w", AgentSkillConfigFile, errors.Join(err, rollbackErr))
			m.setReloadResult(combined)
			return combined
		}
		return fmt.Errorf("reload failed; restored previous %s: %w", AgentSkillConfigFile, err)
	}
	m.setReloadResult(nil)
	return nil
}

type fileSnapshot struct {
	exists bool        // 写入前文件是否存在，决定恢复内容还是删除新建文件。
	data   []byte      // 写入前的原始字节，失败恢复时不重新编码。
	mode   os.FileMode // 写入前的文件权限。
}

func captureFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read existing %s: %w", AgentSkillConfigFile, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("stat existing %s: %w", AgentSkillConfigFile, err)
	}
	return fileSnapshot{exists: true, data: data, mode: info.Mode().Perm()}, nil
}

func (s fileSnapshot) restore(path string) error {
	if s.exists {
		return fileops.AtomicWriteFile(path, s.data, s.mode)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
