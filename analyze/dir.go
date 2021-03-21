package analyze

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// CurrentProgress struct
type CurrentProgress struct {
	mutex           *sync.Mutex
	CurrentItemName string
	ItemCount       int
	TotalSize       int64
}

var concurrencyLimit chan struct{} = make(chan struct{}, 3*runtime.NumCPU())

// ShouldDirBeIgnored whether path should be ignored
type ShouldDirBeIgnored func(path string) bool

// Analyzer is type for dir analyzing function
type Analyzer interface {
	AnalyzeDir(path string, ignore ShouldDirBeIgnored) *Dir
	GetProgressChan() chan CurrentProgress
	GetDoneChan() chan struct{}
	ResetProgress()
}

// ParallelAnalyzer implements Analyzer
type ParallelAnalyzer struct {
	progress     *CurrentProgress
	progressChan chan CurrentProgress
	doneChan     chan struct{}
	wait         sync.WaitGroup
	ignoreDir    ShouldDirBeIgnored
}

// CreateAnalyzer returns Analyzer
func CreateAnalyzer() Analyzer {
	return &ParallelAnalyzer{
		progress: &CurrentProgress{
			mutex:     &sync.Mutex{},
			ItemCount: 0,
			TotalSize: int64(0),
		},
		progressChan: make(chan CurrentProgress, 10),
		doneChan:     make(chan struct{}, 1),
	}
}

// GetProgressChan returns progress
func (a *ParallelAnalyzer) GetProgressChan() chan CurrentProgress {
	return a.progressChan
}

// GetDoneChan returns progress
func (a *ParallelAnalyzer) GetDoneChan() chan struct{} {
	return a.doneChan
}

// ResetProgress returns progress
func (a *ParallelAnalyzer) ResetProgress() {
	a.progress.ItemCount = 0
	a.progress.TotalSize = int64(0)
	a.progress.CurrentItemName = ""
	a.progress.mutex = &sync.Mutex{}
}

// AnalyzeDir analyzes given path
func (a *ParallelAnalyzer) AnalyzeDir(path string, ignore ShouldDirBeIgnored) *Dir {
	a.ignoreDir = ignore
	dir := a.processDir(path)
	dir.BasePath = filepath.Dir(path)
	a.wait.Wait()

	links := make(AlreadyCountedHardlinks, 10)
	dir.UpdateStats(links)

	a.doneChan <- struct{}{}

	return dir
}

func (a *ParallelAnalyzer) processDir(path string) *Dir {
	var (
		file       *File
		err        error
		totalSize  int64
		info       os.FileInfo
		subDirChan chan *Dir = make(chan *Dir)
		dirCount   int       = 0
	)

	files, err := os.ReadDir(path)
	if err != nil {
		log.Print(err.Error())
	}

	dir := &Dir{
		File: &File{
			Name: filepath.Base(path),
			Flag: getDirFlag(err, len(files)),
		},
		ItemCount: 1,
		Files:     make([]Item, 0, len(files)),
	}

	for _, f := range files {
		entryPath := filepath.Join(path, f.Name())
		if f.IsDir() {
			if a.ignoreDir(entryPath) {
				continue
			}
			dirCount += 1

			go func() {
				concurrencyLimit <- struct{}{}
				subdir := a.processDir(entryPath)
				subdir.Parent = dir

				subDirChan <- subdir
				<-concurrencyLimit
			}()
		} else {
			info, err = f.Info()
			if err != nil {
				log.Print(err.Error())
				continue
			}
			file = &File{
				Name:   f.Name(),
				Flag:   getFlag(info),
				Size:   info.Size(),
				Parent: dir,
			}
			setPlatformSpecificAttrs(file, info)

			totalSize += info.Size()

			dir.Files = append(dir.Files, file)
		}
	}

	a.wait.Add(1)
	go func() {
		var sub *Dir

		for i := 0; i < dirCount; i++ {
			sub = <-subDirChan
			dir.Files = append(dir.Files, sub)
		}

		a.wait.Done()
	}()

	a.updateProgress(path, len(files), totalSize)
	return dir
}

func (a *ParallelAnalyzer) updateProgress(path string, itemCount int, totalSize int64) {
	a.progress.mutex.Lock()
	a.progress.CurrentItemName = path
	a.progress.ItemCount += itemCount
	a.progress.TotalSize += totalSize
	select {
	case a.progressChan <- *a.progress:
	default:
	}
	a.progress.mutex.Unlock()
}

func getDirFlag(err error, items int) rune {
	switch {
	case err != nil:
		return '!'
	case items == 0:
		return 'e'
	default:
		return ' '
	}
}

func getFlag(f os.FileInfo) rune {
	switch {
	case f.Mode()&os.ModeSymlink != 0:
		fallthrough
	case f.Mode()&os.ModeSocket != 0:
		return '@'
	default:
		return ' '
	}
}
