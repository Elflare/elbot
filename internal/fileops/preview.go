// 提供公共文件预览快照与提交复核能力，供编辑服务和长期记忆等调用方复用。
// 统一处理文件目标、修订及预览结果，业务内容的生成与说明由调用方负责。
package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"elbot/internal/operationresult"
)

// PreparedFile 保存一次文件预览的目标和批准内容，不持有业务锁或调用上下文。
type PreparedFile struct {
	target string     // 准备时解析的真实文件目标，提交时不得改变。
	result EditResult // 已计算的内容、修订和 diff；写入字节不通过 Preview 暴露。
}

// PrepareFile 为业务生成的文本准备文件更新；路径由调用方确定，不引入工作目录或撤销策略。
// transform 只处理文件文本，编码、换行、diff 和修订由公共文件层统一处理。
func PrepareFile(ctx context.Context, path string, transform func(string) (operationresult.Result[string], error)) (operationresult.Result[*PreparedFile], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	target, err := ResolveFileTarget(path, false)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if target.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: target.Status, Message: target.Message}, nil
	}
	file, err := ReadFile(path, "")
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if file.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: file.Status, Message: file.Message}, nil
	}
	text, err := transform(file.Data.Text)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if text.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: text.Status, Message: text.Message}, nil
	}
	result := prepareFileResult(file.Data, false, text.Data, 3, EditFileOptions{})
	if result.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: result.Status, Message: result.Message}, nil
	}
	checked, err := checkFileTarget(path, target.Data, false)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: checked.Status, Message: checked.Message}, nil
	}
	return operationresult.Result[*PreparedFile]{Status: operationresult.Success, Data: &PreparedFile{target: target.Data, result: result.Data}}, nil
}

// PrepareFileEdit 复用编辑协议准备业务文件；不附加工作目录、会话或撤销策略。
// validate 只验证已经计算出的文本，不重新读取文件或执行写入。
func PrepareFileEdit(ctx context.Context, req EditRequest, validate func(string) (operationresult.Result[struct{}], error)) (operationresult.Result[*PreparedFile], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	target, err := ResolveFileTarget(req.Path, req.Create)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if target.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: target.Status, Message: target.Message}, nil
	}
	if req.Options.MaxInputBytes == 0 {
		req.Options.MaxInputBytes = MaxFileSize
	}
	result, err := EditFileWithOptions(req.Path, req.Encoding, req.ExpectedRevision, req.Create, true, req.ContextLines, req.Edits, req.Options)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: result.Status, Message: result.Message}, nil
	}
	data := result.Data
	if validate != nil {
		text, _, _, err := DecodeBytes(data.NewBytes, data.Encoding)
		if err != nil {
			return operationresult.Result[*PreparedFile]{}, err
		}
		checked, err := validate(text)
		if err != nil {
			return operationresult.Result[*PreparedFile]{}, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[*PreparedFile]{Status: checked.Status, Message: checked.Message}, nil
		}
	}
	checked, err := checkFileTarget(req.Path, target.Data, data.Created)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: checked.Status, Message: checked.Message}, nil
	}
	return operationresult.Result[*PreparedFile]{Status: operationresult.Success, Data: &PreparedFile{target: target.Data, result: data}}, nil
}

// PrepareFileWrite 固定业务生成的完整 UTF-8 文本，支持已有文件和新建文件。
// 相同内容仍可提交，使调用方能够继续执行配置重载等业务操作。
func PrepareFileWrite(ctx context.Context, path, content string) (operationresult.Result[*PreparedFile], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	target, err := ResolveFileTarget(path, true)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if target.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: target.Status, Message: target.Message}, nil
	}
	file, created, err := ReadOrCreateFile(path, "", true, false)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if file.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: file.Status, Message: file.Message}, nil
	}
	data := []byte(content)
	diff, diffAvailable := editDiff(path, file.Data.Text, content, len(file.Data.Bytes), len(data), 3, MaxFileSize)
	result := EditResult{
		DryRun: true, Path: path, Created: created, Encoding: "utf-8",
		RevisionBefore: ContentRevision(file.Data.Bytes), RevisionAfter: ContentRevision(data),
		Diff: diff, DiffAvailable: diffAvailable, NewBytes: data,
	}
	checked, err := checkFileTarget(path, target.Data, created)
	if err != nil {
		return operationresult.Result[*PreparedFile]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[*PreparedFile]{Status: checked.Status, Message: checked.Message}, nil
	}
	return operationresult.Result[*PreparedFile]{Status: operationresult.Success, Data: &PreparedFile{target: target.Data, result: result}}, nil
}

// Preview 返回只读展示数据，不泄漏批准写入的字节。
func (p *PreparedFile) Preview() EditResult {
	result := p.result
	result.NewBytes = nil
	return result
}

// Commit 复核真实目标与原修订，只提交准备时确定的内容，不重新执行业务转换。
func (p *PreparedFile) Commit(ctx context.Context) (operationresult.Result[EditResult], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	path, created := p.result.Path, p.result.Created
	checked, err := checkFileTarget(path, p.target, created)
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: checked.Status, Message: checked.Message}, nil
	}
	var previous os.FileInfo
	mode := os.FileMode(0o644)
	if created {
		checked, err := checkFileState(path, true, nil)
		if err != nil {
			return operationresult.Result[EditResult]{}, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[EditResult]{Status: checked.Status, Message: checked.Message}, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return operationresult.Result[EditResult]{}, err
		}
	} else {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return operationresult.Result[EditResult]{Status: operationresult.NotFound, Message: fmt.Sprintf("file %q not found", path)}, nil
		}
		if err != nil {
			return operationresult.Result[EditResult]{}, err
		}
		if info.Size() > MaxFileSize {
			return operationresult.Result[EditResult]{Status: operationresult.Rejected, Message: fmt.Sprintf("file too large: %d bytes exceeds %d", info.Size(), MaxFileSize)}, nil
		}
		inspected, err := inspectFileState(path, false, p.result.RevisionBefore, info.Size())
		if err != nil {
			return operationresult.Result[EditResult]{}, err
		}
		if inspected.Status != operationresult.Success {
			return operationresult.Result[EditResult]{Status: inspected.Status, Message: inspected.Message}, nil
		}
		previous = inspected.Data
		mode = previous.Mode()
	}
	committed, err := atomicWriteFileResult(path, p.result.NewBytes, mode, func() (operationresult.Result[struct{}], error) {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		checked, err := checkFileTarget(path, p.target, created)
		if err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		return checkFileState(path, created, previous)
	})
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	if committed.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: committed.Status, Message: committed.Message}, nil
	}
	result := p.Preview()
	result.DryRun = false
	return operationresult.Result[EditResult]{Status: operationresult.Success, Data: result}, nil
}

// prepareFileResult 为普通编辑和业务文本更新共用编码、修订与差异计算。
func prepareFileResult(file File, created bool, text string, contextLines int, options EditFileOptions) operationresult.Result[EditResult] {
	oldText := NormalizeEditText(file.Text)
	newText := NormalizeEditText(text)
	if newText == oldText {
		return operationresult.Result[EditResult]{Status: operationresult.Rejected, Message: "edit produced no changes"}
	}
	outputText := RestoreLineEndings(newText, file.LineEnding)
	newBytes, err := EncodeText(outputText, file.Encoding, file.BOM)
	if err != nil {
		return operationresult.Result[EditResult]{Status: operationresult.InvalidArguments, Message: err.Error()}
	}
	if options.MaxOutputBytes > 0 && int64(len(newBytes)) > options.MaxOutputBytes {
		return operationresult.Result[EditResult]{Status: operationresult.Rejected, Message: fmt.Sprintf("edited file too large: %d bytes exceeds %d", len(newBytes), options.MaxOutputBytes)}
	}
	diff, diffAvailable := editDiff(file.Path, oldText, newText, len(file.Bytes), len(newBytes), NormalizeContextLines(contextLines), options.DetailedDiffMaxBytes)
	return operationresult.Result[EditResult]{Status: operationresult.Success, Data: EditResult{
		DryRun: true, Path: file.Path, Created: created, Encoding: file.Encoding,
		RevisionBefore: ContentRevision(file.Bytes), RevisionAfter: ContentRevision(newBytes),
		Diff: diff, DiffAvailable: diffAvailable, NewBytes: newBytes,
	}}
}
