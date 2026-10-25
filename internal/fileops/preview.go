// 提供公共文件预览快照与提交复核能力，供编辑服务和长期记忆等调用方复用。
// 统一处理文件目标、修订及预览结果，业务内容的生成与说明由调用方负责。
package fileops

import (
	"context"
	"fmt"
)

// PreparedFile 保存一次文件预览的目标和批准内容，不持有业务锁或调用上下文。
type PreparedFile struct {
	target string     // 准备时解析的真实文件目标，提交时不得改变。
	result EditResult // 已计算的内容、修订和 diff；写入字节不通过 Preview 暴露。
}

// PrepareFile 为业务生成的文本准备文件更新；路径由调用方确定，不引入工作目录或撤销策略。
// transform 只处理文件文本，编码、换行、diff 和修订由公共文件层统一处理。
func PrepareFile(ctx context.Context, path string, transform func(string) (string, error)) (*PreparedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := ResolveFileTarget(path, false)
	if err != nil {
		return nil, err
	}
	file, err := ReadFile(path, "")
	if err != nil {
		return nil, err
	}
	text, err := transform(file.Text)
	if err != nil {
		return nil, err
	}
	result, err := prepareFileResult(file, false, text, 3, EditFileOptions{})
	if err != nil {
		return nil, err
	}
	if err := checkFileTarget(path, target, false); err != nil {
		return nil, err
	}
	return &PreparedFile{target: target, result: result}, nil
}

// Preview 返回只读展示数据，不泄漏批准写入的字节。
func (p *PreparedFile) Preview() EditResult {
	result := p.result
	result.NewBytes = nil
	return result
}

// Commit 复核真实目标与原修订，只提交准备时确定的内容，不重新执行业务转换。
func (p *PreparedFile) Commit(ctx context.Context) (EditResult, error) {
	if err := WriteFileWithRevision(ctx, p.result.Path, p.target, p.result.RevisionBefore, p.result.NewBytes); err != nil {
		return EditResult{}, err
	}
	result := p.Preview()
	result.DryRun = false
	return result, nil
}

// prepareFileResult 为普通编辑和业务文本更新共用编码、修订与差异计算。
func prepareFileResult(file File, created bool, text string, contextLines int, options EditFileOptions) (EditResult, error) {
	oldText := NormalizeEditText(file.Text)
	newText := NormalizeEditText(text)
	if newText == oldText {
		return EditResult{}, fmt.Errorf("edit produced no changes")
	}
	outputText := RestoreLineEndings(newText, file.LineEnding)
	newBytes, err := EncodeText(outputText, file.Encoding, file.BOM)
	if err != nil {
		return EditResult{}, err
	}
	if options.MaxOutputBytes > 0 && int64(len(newBytes)) > options.MaxOutputBytes {
		return EditResult{}, fmt.Errorf("edited file too large: %d bytes exceeds %d", len(newBytes), options.MaxOutputBytes)
	}
	return EditResult{
		DryRun: true, Path: file.Path, Created: created, Encoding: file.Encoding,
		RevisionBefore: ContentRevision(file.Bytes), RevisionAfter: ContentRevision(newBytes),
		Diff:     editDiff(file.Path, oldText, newText, len(file.Bytes), len(newBytes), NormalizeContextLines(contextLines), options.DetailedDiffMaxBytes),
		NewBytes: newBytes,
	}, nil
}
