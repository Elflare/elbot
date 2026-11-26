package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
)

// root 是所有会话共用的草稿根目录；只在首次创建文件时建立会话子目录。
func draft(root string) share.ToolDefinition {
	return share.NewBuilder("draft").ForegroundOnly().SuperadminOnly().Weavable().Tags("files").Risk(share.RiskMedium).
		Description("草稿，可随时读写和执行；path=绝对路径；无参→[(name, path)]；两参→创建并返回绝对路径；重名→提示，不覆盖。").
		String("name", "仅name→(content, path)。").String("content", "创建内容。").
		Build(func(ctx context.Context, req share.CallRequest, info share.Info) (prep operationresult.Result[*share.PreparedCall], err error) {
			var args struct {
				Name    *string `json:"name"`
				Content *string `json:"content"`
			}
			var name string
			switch err := decodeStrictFileArgs(req.Arguments, &args); {
			case err != nil:
				prep.Message = err.Error()
			case args.Name == nil && args.Content != nil:
				prep.Message = "创建草稿需要 name"
			case args.Name != nil:
				name = *args.Name
				if !filepath.IsLocal(name) || name == "." || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\:\x00") {
					prep.Message = "name 必须是单个文件名"
				}
			}
			if prep.Message != "" {
				prep.Status = operationresult.InvalidArguments
				return prep, nil
			}
			execution, _ := contextinfo.ExecutionFromContext(ctx)
			dir, err := filepath.Abs(filepath.Join(root, execution.SessionID))
			if err != nil {
				return prep, err
			}
			path := filepath.Join(dir, name)
			level := share.RiskLow
			details := []share.ConfirmationDetailBlock{share.FieldsConfirmationDetail(share.ConfirmationField{Key: "path", Label: "位置", Value: path})}
			if args.Content != nil {
				level = info.Risk
				details = append(details, share.TextConfirmationDetail(*args.Content))
			}
			prep.Status = operationresult.Success
			prep.Data = share.NewPreparedCall(req, share.RiskAssessment{Level: level}, details, func(context.Context) (result operationresult.Result[*share.Result], summary string, err error) {
				var text string
				switch {
				case name == "":
					entries, err := os.ReadDir(dir)
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						return result, path, err
					}
					files := make([][2]string, 0, len(entries))
					for _, entry := range entries {
						if entry.Type().IsRegular() {
							files = append(files, [2]string{entry.Name(), filepath.Join(dir, entry.Name())})
						}
					}
					data, _ := json.Marshal(files)
					text = string(data)
				case args.Content == nil:
					stat, err := os.Lstat(path)
					if errors.Is(err, fs.ErrNotExist) {
						result.Status, result.Message = operationresult.NotFound, "草稿不存在"
						return result, path, nil
					}
					if err != nil {
						return result, path, err
					}
					if !stat.Mode().IsRegular() {
						result.Status, result.Message = operationresult.InvalidTarget, "草稿不是普通文件"
						return result, path, nil
					}
					content, err := os.ReadFile(path)
					if err != nil {
						return result, path, err
					}
					data, _ := json.Marshal([2]string{string(content), path})
					text = string(data)
				default:
					if err := os.MkdirAll(dir, 0700); err != nil {
						return result, path, err
					}
					file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if errors.Is(err, fs.ErrExist) {
						result.Status, result.Message = operationresult.Conflict, "草稿已存在，未覆盖"
						return result, path, nil
					}
					if err != nil {
						return result, path, err
					}
					_, err = file.WriteString(*args.Content)
					if err := errors.Join(err, file.Close()); err != nil {
						return result, path, err
					}
					text = path
				}
				result.Status, result.Data = operationresult.Success, &share.Result{Content: text}
				return result, path, nil
			})
			return prep, nil
		})
}
