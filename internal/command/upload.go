package command

import (
	"fmt"
	"github.com/tickstep/cloudpan189-api/cloudpan"
	"github.com/tickstep/cloudpan189-go/cmder/cmdtable"
	"github.com/tickstep/cloudpan189-go/cmder/cmdutil"
	"github.com/tickstep/cloudpan189-go/internal/config"
	"github.com/tickstep/cloudpan189-go/internal/functions/panupload"
	"github.com/tickstep/cloudpan189-go/internal/localfile"
	"github.com/tickstep/cloudpan189-go/internal/taskframework"
	"github.com/tickstep/library-go/converter"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultUploadMaxAllParallel 默认所有文件并发上传数量，即可以同时并发上传多少个文件
	DefaultUploadMaxAllParallel = 1
	// DefaultUploadMaxRetry 默认上传失败最大重试次数
	DefaultUploadMaxRetry = 3
)

type (
	// UploadOptions 上传可选项
	UploadOptions struct {
		AllParallel   int // 所有文件并发上传数量，即可以同时并发上传多少个文件
		Parallel      int // 单个文件并发上传数量
		MaxRetry      int
		NoRapidUpload bool
		NoSplitFile   bool // 禁用分片上传
		ShowProgress  bool
		IsOverwrite   bool // 覆盖已存在的文件，如果同名文件已存在则移到回收站里
	}
)

// RunUpload 执行文件上传
func RunUpload(localPaths []string, savePath string, opt *UploadOptions) {
	activeUser := GetActiveUser()
	if opt == nil {
		opt = &UploadOptions{}
	}

	// 检测opt
	if opt.AllParallel <= 0 {
		opt.AllParallel = config.Config.MaxUploadParallel
	}
	if opt.Parallel <= 0 {
		opt.Parallel = 1
	}

	if opt.MaxRetry < 0 {
		opt.MaxRetry = DefaultUploadMaxRetry
	}

	savePath = activeUser.PathJoin(savePath)
	_, err1 := activeUser.PanClient().FileInfoByPath(savePath)
	if err1 != nil {
		fmt.Printf("警告: 上传文件, 获取云盘路径 %s 错误, %s\n", savePath, err1)
	}

	switch len(localPaths) {
	case 0:
		fmt.Printf("本地路径为空\n")
		return
	}

	// 打开上传状态
	uploadDatabase, err := panupload.NewUploadingDatabase()
	if err != nil {
		fmt.Printf("打开上传未完成数据库错误: %s\n", err)
		return
	}
	defer uploadDatabase.Close()

	var (
		// 使用 task framework
		executor = &taskframework.TaskExecutor{
			IsFailedDeque: true, // 失败统计
		}
		subSavePath string
		// 统计
		statistic = &panupload.UploadStatistic{}

		folderCreateMutex = &sync.Mutex{}
	)
	executor.SetParallel(opt.AllParallel)

	statistic.StartTimer() // 开始计时

	for k := range localPaths {
		walkedFiles, err := cmdutil.WalkDir(localPaths[k], "")
		if err != nil {
			fmt.Printf("警告: 遍历错误: %s\n", err)
			continue
		}

		for k3 := range walkedFiles {
			var localPathDir string
			// 针对 windows 的目录处理
			if os.PathSeparator == '\\' {
				walkedFiles[k3] = cmdutil.ConvertToUnixPathSeparator(walkedFiles[k3])
				localPathDir = cmdutil.ConvertToUnixPathSeparator(filepath.Dir(localPaths[k]))
			} else {
				localPathDir = filepath.Dir(localPaths[k])
			}

			// 避免去除文件名开头的"."
			if localPathDir == "." {
				localPathDir = ""
			}

			subSavePath = strings.TrimPrefix(walkedFiles[k3], localPathDir)

			info := executor.Append(&panupload.UploadTaskUnit{
				LocalFileChecksum: localfile.NewLocalFileEntity(walkedFiles[k3]),
				SavePath:          path.Clean(savePath + cloudpan.PathSeparator + subSavePath),
				PanClient:         activeUser.PanClient(),
				UploadingDatabase: uploadDatabase,
				FolderCreateMutex: folderCreateMutex,
				Parallel:          opt.Parallel,
				NoRapidUpload:     opt.NoRapidUpload,
				NoSplitFile:       opt.NoSplitFile,
				UploadStatistic:   statistic,
				ShowProgress: opt.ShowProgress,
				IsOverwrite: opt.IsOverwrite,
			}, opt.MaxRetry)
			fmt.Printf("[%s] 加入上传队列: %s\n", info.Id(), walkedFiles[k3])
		}
	}

	// 没有添加任何任务
	if executor.Count() == 0 {
		fmt.Printf("未检测到上传的文件.\n")
		return
	}

	// 执行上传任务
	executor.Execute()

	fmt.Printf("\n")
	fmt.Printf("上传结束, 时间: %s, 总大小: %s\n", statistic.Elapsed()/1e6*1e6, converter.ConvertFileSize(statistic.TotalSize()))

	// 输出上传失败的文件列表
	failedList := executor.FailedDeque()
	if failedList.Size() != 0 {
		fmt.Printf("以下文件上传失败: \n")
		tb := cmdtable.NewTable(os.Stdout)
		for e := failedList.Shift(); e != nil; e = failedList.Shift() {
			item := e.(*taskframework.TaskInfoItem)
			tb.Append([]string{item.Info.Id(), item.Unit.(*panupload.UploadTaskUnit).LocalFileChecksum.Path})
		}
		tb.Render()
	}
}
