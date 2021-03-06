// Copyright (c) 2018, The GoKi Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package giv

import (
	"bufio"
	"bytes"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/goki/gi/gi"
	"github.com/goki/gi/histyle"
	"github.com/goki/gi/oswin"
	"github.com/goki/gi/oswin/dnd"
	"github.com/goki/gi/oswin/mimedata"
	"github.com/goki/gi/units"
	"github.com/goki/ki"
	"github.com/goki/ki/ints"
	"github.com/goki/ki/kit"
)

// FileTree is the root of a tree representing files in a given directory (and
// subdirectories thereof), and has some overall management state for how to
// view things.  The FileTree can be viewed by a TreeView to provide a GUI
// interface into it.
type FileTree struct {
	FileNode
	OpenDirs  OpenDirMap   `desc:"records which directories within the tree (encoded using paths relative to root) are open (i.e., have been opened by the user) -- can persist this to restore prior view of a tree"`
	DirsOnTop bool         `desc:"if true, then all directories are placed at the top of the tree view -- otherwise everything is alpha sorted"`
	NodeType  reflect.Type `desc:"type of node to create -- defaults to giv.FileNode but can use custom node types"`
}

var KiT_FileTree = kit.Types.AddType(&FileTree{}, FileTreeProps)

var FileTreeProps = ki.Props{}

// OpenPath opens a filetree at given directory path -- reads all the files at
// given path into this tree -- uses config children to preserve extra info
// already stored about files.  Only paths listed in OpenDirs will be opened.
func (ft *FileTree) OpenPath(path string) {
	ft.FRoot = ft // we are our own root..
	if ft.NodeType == nil {
		ft.NodeType = KiT_FileNode
	}
	ft.OpenDirs.ClearFlags()
	ft.ReadDir(path)
}

// UpdateNewFile should be called with path to a new file that has just been
// created -- will update view to show that file, and if that file doesn't
// exist, it updates the directory containing that file
func (ft *FileTree) UpdateNewFile(filename string) {
	ft.OpenDirsTo(filename)
	fpath, _ := filepath.Split(filename)
	fpath = filepath.Clean(fpath)
	if fn, ok := ft.FindFile(filename); ok {
		// fmt.Printf("updating node for file: %v\n", filename)
		fn.UpdateNode()
	} else if fn, ok := ft.FindFile(fpath); ok {
		// fmt.Printf("updating node for path: %v\n", fpath)
		fn.UpdateNode()
	} else {
		log.Printf("giv.FileTree UpdateNewFile: no node found for path to update: %v\n", filename)
	}
}

// IsDirOpen returns true if given directory path is open (i.e., has been
// opened in the view)
func (ft *FileTree) IsDirOpen(fpath gi.FileName) bool {
	if fpath == ft.FPath { // we are always open
		return true
	}
	return ft.OpenDirs.IsOpen(ft.RelPath(fpath))
}

// SetDirOpen sets the given directory path to be open
func (ft *FileTree) SetDirOpen(fpath gi.FileName) {
	ft.OpenDirs.SetOpen(ft.RelPath(fpath))
}

// SetDirClosed sets the given directory path to be closed
func (ft *FileTree) SetDirClosed(fpath gi.FileName) {
	ft.OpenDirs.SetClosed(ft.RelPath(fpath))
}

//////////////////////////////////////////////////////////////////////////////
//    FileNode

// FileNodeHiStyle is the default style for syntax highlighting to use for
// file node buffers
var FileNodeHiStyle = histyle.StyleDefault

// FileNode represents a file in the file system -- the name of the node is
// the name of the file.  Folders have children containing further nodes.
type FileNode struct {
	ki.Node
	FPath gi.FileName `desc:"full path to this file"`
	Info  FileInfo    `desc:"full standard file info about this file"`
	Buf   *TextBuf    `json:"-" xml:"-" desc:"file buffer for editing this file"`
	FRoot *FileTree   `json:"-" xml:"-" desc:"root of the tree -- has global state"`
}

var KiT_FileNode = kit.Types.AddType(&FileNode{}, FileNodeProps)

// IsDir returns true if file is a directory (folder)
func (fn *FileNode) IsDir() bool {
	return fn.Info.IsDir()
}

// IsSymLink returns true if file is a symlink
func (fn *FileNode) IsSymLink() bool {
	return fn.HasFlag(int(FileNodeSymLink))
}

// IsExec returns true if file is an executable file
func (fn *FileNode) IsExec() bool {
	return fn.Info.IsExec()
}

// IsOpen returns true if file is flagged as open
func (fn *FileNode) IsOpen() bool {
	return fn.HasFlag(int(FileNodeOpen))
}

// SetOpen sets the open flag
func (fn *FileNode) SetOpen() {
	fn.SetFlag(int(FileNodeOpen))
}

// SetClosed clears the open flag
func (fn *FileNode) SetClosed() {
	fn.ClearFlag(int(FileNodeOpen))
}

// IsChanged returns true if the file is open and has been changed (edited) since last save
func (fn *FileNode) IsChanged() bool {
	if fn.Buf != nil && fn.Buf.IsChanged() {
		return true
	}
	return false
}

// IsAutoSave returns true if file is an auto-save file (starts and ends with #)
func (fn *FileNode) IsAutoSave() bool {
	if strings.HasPrefix(fn.Info.Name, "#") && strings.HasSuffix(fn.Info.Name, "#") {
		return true
	}
	return false
}

// MyRelPath returns the relative path from root for this node
func (fn *FileNode) MyRelPath() string {
	rpath, err := filepath.Rel(string(fn.FRoot.FPath), string(fn.FPath))
	if err != nil {
		log.Printf("giv.FileNode RelPath error: %v\n", err.Error())
	}
	return rpath
}

// ReadDir reads all the files at given directory into this directory node --
// uses config children to preserve extra info already stored about files. The
// root node represents the directory at the given path.  Returns os.Stat
// error if path cannot be accessed.
func (fn *FileNode) ReadDir(path string) error {
	_, fnm := filepath.Split(path)
	fn.SetName(fnm)
	pth, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fn.FPath = gi.FileName(pth)
	err = fn.Info.InitFile(string(fn.FPath))
	if err != nil {
		log.Printf("giv.FileTree: could not read directory: %v err: %v\n", fn.FPath, err)
		return err
	}
	fn.SetOpen()

	config := fn.ConfigOfFiles(path)
	mods, updt := fn.ConfigChildren(config, false) // NOT unique names
	// always go through kids, regardless of mods
	for _, sfk := range fn.Kids {
		sf := sfk.Embed(KiT_FileNode).(*FileNode)
		sf.FRoot = fn.FRoot
		fp := filepath.Join(path, sf.Nm)
		sf.SetNodePath(fp)
	}
	if mods {
		fn.UpdateEnd(updt)
	}
	return nil
}

// ConfigOfFiles returns a type-and-name list for configuring nodes based on
// files immediately within given path
func (fn *FileNode) ConfigOfFiles(path string) kit.TypeAndNameList {
	config1 := kit.TypeAndNameList{}
	config2 := kit.TypeAndNameList{}
	typ := fn.FRoot.NodeType
	filepath.Walk(path, func(pth string, info os.FileInfo, err error) error {
		if err != nil {
			emsg := fmt.Sprintf("giv.FileNode ConfigFilesIn Path %q: Error: %v", path, err)
			log.Println(emsg)
			return nil // ignore
		}
		if pth == path { // proceed..
			return nil
		}
		_, fnm := filepath.Split(pth)
		if fn.FRoot.DirsOnTop {
			if info.IsDir() {
				config1.Add(typ, fnm)
			} else {
				config2.Add(typ, fnm)
			}
		} else {
			config1.Add(typ, fnm)
		}
		if info.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if fn.FRoot.DirsOnTop {
		for _, tn := range config2 {
			config1 = append(config1, tn)
		}
	}
	return config1
}

// SetNodePath sets the path for given node and updates it based on associated file
func (fn *FileNode) SetNodePath(path string) error {
	pth, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fn.FPath = gi.FileName(pth)
	return fn.UpdateNode()
}

// UpdateNode updates information in node based on its associated file in FPath
func (fn *FileNode) UpdateNode() error {
	err := fn.Info.InitFile(string(fn.FPath))
	if err != nil {
		emsg := fmt.Errorf("giv.FileNode UpdateNode Path %q: Error: %v", fn.FPath, err)
		log.Println(emsg)
		return emsg
	}
	if fn.IsDir() {
		if fn.FRoot.IsDirOpen(fn.FPath) {
			fn.ReadDir(string(fn.FPath)) // keep going down..
		}
	}
	return nil
}

// OpenDir opens given directory node
func (fn *FileNode) OpenDir() {
	fn.SetOpen()
	fn.FRoot.SetDirOpen(fn.FPath)
	fn.UpdateNode()
}

// CloseDir closes given directory node -- updates memory state
func (fn *FileNode) CloseDir() {
	fn.SetClosed()
	fn.FRoot.SetDirClosed(fn.FPath)
	// todo: do anything with open files within directory??
}

// OpenBuf opens the file in its buffer if it is not already open.
// returns true if file is newly opened
func (fn *FileNode) OpenBuf() (bool, error) {
	if fn.IsDir() {
		err := fmt.Errorf("giv.FileNode cannot open directory in editor: %v", fn.FPath)
		log.Println(err.Error())
		return false, err
	}
	if fn.Buf != nil {
		if fn.Buf.Filename == fn.FPath { // close resets filename
			return false, nil
		}
	} else {
		fn.Buf = &TextBuf{}
		fn.Buf.InitName(fn.Buf, fn.Nm)
	}
	fn.Buf.Hi.Style = FileNodeHiStyle
	return true, fn.Buf.Open(fn.FPath)
}

// CloseBuf closes the file in its buffer if it is open -- returns true if closed
func (fn *FileNode) CloseBuf() bool {
	if fn.Buf == nil {
		return false
	}
	fn.Buf.Close(nil)
	fn.Buf = nil
	return true
}

// RelPath returns the relative path from node for given full path
func (fn *FileNode) RelPath(fpath gi.FileName) string {
	rpath, err := filepath.Rel(string(fn.FPath), string(fpath))
	if err != nil {
		log.Printf("giv.FileNode RelPath error: %v\n", err.Error())
		return ""
	}
	return rpath
}

// OpenDirsTo opens all the directories above the given filename, and returns the node
// for element at given path (can be a file or directory itself -- not opened -- just returned)
func (fn *FileNode) OpenDirsTo(path string) (*FileNode, error) {
	pth, err := filepath.Abs(path)
	if err != nil {
		log.Printf("giv.FileNode OpenDirsTo path %v could not be turned into an absolute path: %v\n", path, err)
		return nil, err
	}
	rpath := fn.RelPath(gi.FileName(pth))
	if rpath == "." {
		return fn, nil
	}
	if rpath == "" {
		err := fmt.Errorf("giv.FileNode OpenDirsTo path %v is not within file tree path: %v", pth, fn.FPath)
		log.Println(err)
		return nil, err
	}
	dirs := strings.Split(rpath, string(filepath.Separator))
	cfn := fn
	sz := len(dirs)
	for i := 0; i < sz; i++ {
		dr := dirs[i]
		sfni, ok := cfn.ChildByName(dr, 0)
		if !ok {
			if i == sz-1 { // ok for terminal -- might not exist yet
				return cfn, nil
			} else {
				err := fmt.Errorf("giv.FileNode could not find node %v in: %v", dr, cfn.FPath)
				log.Println(err)
				return nil, err
			}
		}
		sfn := sfni.Embed(KiT_FileNode).(*FileNode)
		if sfn.IsDir() || i == sz-1 {
			if i < sz-1 && !sfn.IsOpen() {
				sfn.OpenDir()
			} else {
				cfn = sfn
			}
		} else {
			err := fmt.Errorf("giv.FileNode non-terminal node %v is not a directory in: %v", dr, cfn.FPath)
			log.Println(err)
			return nil, err
		}
		cfn = sfn
	}
	return cfn, nil
}

// FindFile finds first node representing given file (false if not found) --
// looks for full path names that have the given string as their suffix, so
// you can include as much of the path (including whole thing) as is relevant
// to disambiguate.  See FilesMatching for a list of files that match a given
// string.
func (fn *FileNode) FindFile(fnm string) (*FileNode, bool) {
	if fnm == "" {
		return nil, false
	}
	fneff := fnm
	if fneff[:2] == ".." { // relative path -- get rid of it and just look for relative part
		dirs := strings.Split(fneff, string(filepath.Separator))
		for i, dr := range dirs {
			if dr != ".." {
				fneff = filepath.Join(dirs[i:]...)
				break
			}
		}
	}

	if strings.HasPrefix(fneff, string(fn.FPath)) { // full path
		ffn, err := fn.OpenDirsTo(fneff)
		if err == nil {
			return ffn, true
		}
		return nil, false
	}

	var ffn *FileNode
	found := false
	fn.FuncDownMeFirst(0, fn, func(k ki.Ki, level int, d interface{}) bool {
		sfn := k.Embed(KiT_FileNode).(*FileNode)
		if strings.HasSuffix(string(sfn.FPath), fneff) {
			ffn = sfn
			found = true
			return false
		}
		return true
	})
	return ffn, found
}

// FilesMatching returns list of all nodes whose file name contains given
// string (no regexp) -- ignoreCase transforms everything into lowercase
func (fn *FileNode) FilesMatching(match string, ignoreCase bool) []*FileNode {
	mls := make([]*FileNode, 0)
	if ignoreCase {
		match = strings.ToLower(match)
	}
	fn.FuncDownMeFirst(0, fn, func(k ki.Ki, level int, d interface{}) bool {
		sfn := k.Embed(KiT_FileNode).(*FileNode)
		if ignoreCase {
			nm := strings.ToLower(sfn.Nm)
			if strings.Contains(nm, match) {
				mls = append(mls, sfn)
			}
		} else {
			if strings.Contains(sfn.Nm, match) {
				mls = append(mls, sfn)
			}
		}
		return true
	})
	return mls
}

// FileNodeNameCount is used to report counts of different string-based things
// in the file tree
type FileNodeNameCount struct {
	Name  string
	Count int
}

// FileExtCounts returns a count of all the different file extensions, sorted
// from highest to lowest
func (fn *FileNode) FileExtCounts() []FileNodeNameCount {
	cmap := make(map[string]int, 20)
	fn.FuncDownMeFirst(0, fn, func(k ki.Ki, level int, d interface{}) bool {
		sfn := k.Embed(KiT_FileNode).(*FileNode)
		ext := strings.ToLower(filepath.Ext(sfn.Nm))
		if ec, has := cmap[ext]; has {
			cmap[ext] = ec + 1
		} else {
			cmap[ext] = 1
		}
		return true
	})
	ecs := make([]FileNodeNameCount, len(cmap))
	idx := 0
	for key, val := range cmap {
		ecs[idx] = FileNodeNameCount{Name: key, Count: val}
		idx++
	}
	sort.Slice(ecs, func(i, j int) bool {
		return ecs[i].Count > ecs[j].Count
	})
	return ecs
}

//////////////////////////////////////////////////////////////////////////////
//    File ops

// Duplicate creates a copy of given file -- only works for regular files, not
// directories
func (fn *FileNode) DuplicateFile() error {
	err := fn.Info.Duplicate()
	if err == nil && fn.Par != nil {
		fnp := fn.Par.Embed(KiT_FileNode).(*FileNode)
		fnp.UpdateNode()
	}
	return err
}

// DeleteFile deletes this file
func (fn *FileNode) DeleteFile() error {
	err := fn.Info.Delete()
	if err == nil {
		fn.Delete(true) // we're done
	}
	return err
}

// RenameFile renames file to new name
func (fn *FileNode) RenameFile(newpath string) error {
	err := fn.Info.Rename(newpath)
	if err == nil {
		fn.FPath = gi.FileName(fn.Info.Path)
		fn.SetName(fn.Info.Name)
		fn.UpdateSig()
	}
	return err
}

// NewFile makes a new file in given selected directory node
func (fn *FileNode) NewFile(filename string) {
	np := filepath.Join(string(fn.FPath), filename)
	_, err := os.Create(np)
	if err != nil {
		gi.PromptDialog(nil, gi.DlgOpts{Title: "Couldn't Make File", Prompt: fmt.Sprintf("Could not make new file at: %v, err: %v", np, err)}, true, false, nil, nil)
		return
	}
	fn.FRoot.UpdateNewFile(np)
}

// NewFolder makes a new folder (directory) in given selected directory node
func (fn *FileNode) NewFolder(foldername string) {
	np := filepath.Join(string(fn.FPath), foldername)
	err := os.MkdirAll(np, 0775)
	if err != nil {
		emsg := fmt.Sprintf("giv.FileNode at: %q: Error: %v", fn.FPath, err)
		gi.PromptDialog(nil, gi.DlgOpts{Title: "Couldn't Make Folder", Prompt: emsg}, true, false, nil, nil)
		return
	}
	fn.FRoot.UpdateNewFile(string(fn.FPath))
}

// CopyFileToDir copies given file path into node that is a directory
// prompts before overwriting any existing
func (fn *FileNode) CopyFileToDir(filename string, perm os.FileMode) {
	_, sfn := filepath.Split(filename)
	tpath := filepath.Join(string(fn.FPath), sfn)
	if _, err := os.Stat(tpath); os.IsNotExist(err) {
		CopyFile(tpath, filename, perm)
	} else {
		gi.ChoiceDialog(nil, gi.DlgOpts{Title: "File Exists, Overwrite?",
			Prompt: fmt.Sprintf("File: %v exists, do you want to overwrite it with: %v?", tpath, filename)},
			[]string{"No, Cancel", "Yes, Overwrite"},
			fn.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
				switch sig {
				case 0:
					// cancel
				case 1:
					CopyFile(tpath, filename, perm)
				}
			})
	}
}

// CopyFileToFile copies given file path into node that is an existing file
// prompts before doing so
func (fn *FileNode) CopyFileToFile(filename string, perm os.FileMode) {
	tpath := string(fn.FPath)
	gi.ChoiceDialog(nil, gi.DlgOpts{Title: "Overwrite?",
		Prompt: fmt.Sprintf("Are you sure you want to overwrite file: %v with: %v?", tpath, filename)},
		[]string{"No, Cancel", "Yes, Overwrite"},
		fn.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			switch sig {
			case 0:
			// cancel
			case 1:
				CopyFile(tpath, filename, perm)
			}
		})
}

//////////////////////////////////////////////////////////////////////////
//  Search

// FileSearchMatch records one match for search within file
type FileSearchMatch struct {
	Reg  TextRegion `desc:"region surrounding the match"`
	Text []byte     `desc:"text surrounding the match, at most FileSearchContext on either side (within a single line)"`
}

// FileSearchContext is how much text to include on either side of the search match
var FileSearchContext = 30

// FileSearch looks for a string (no regexp) within a file, in a
// case-sensitive way, returning number of occurences and specific match
// position list -- column positions are in bytes, not runes.
func FileSearch(filename string, find []byte, ignoreCase bool) (int, []FileSearchMatch) {
	fp, err := os.Open(filename)
	if err != nil {
		log.Printf("gide.FileSearch file open error: %v\n", err)
		return 0, nil
	}
	defer fp.Close()
	return ByteBufSearch(fp, find, ignoreCase)
}

// ByteBufSearch looks for a string (no regexp) within a byte buffer, with
// given case-sensitivity, returning number of occurences and specific match
// position list -- column positions are in bytes, not runes.
func ByteBufSearch(reader io.Reader, find []byte, ignoreCase bool) (int, []FileSearchMatch) {
	fsz := len(find)
	if fsz == 0 {
		return 0, nil
	}
	findeff := find
	if ignoreCase {
		findeff = bytes.ToLower(find)
	}
	cnt := 0
	var matches []FileSearchMatch
	scan := bufio.NewScanner(reader)
	ln := 0
	mst := []byte("<mark>")
	mstsz := len(mst)
	med := []byte("</mark>")
	medsz := len(med)
	for scan.Scan() {
		bo := scan.Bytes() // note: temp -- must copy!
		b := bo
		if ignoreCase {
			b = bytes.ToLower(bo)
		}
		sz := len(b)
		ci := 0
		for ci < sz {
			i := bytes.Index(b[ci:], findeff)
			if i < 0 {
				break
			}
			i += ci
			ci = i + fsz
			reg := NewTextRegion(ln, i, ln, ci)
			cist := ints.MaxInt(i-FileSearchContext, 0)
			cied := ints.MinInt(ci+FileSearchContext, sz)
			tlen := mstsz + medsz + cied - cist
			txt := make([]byte, tlen)
			copy(txt, bo[cist:i])
			ti := i - cist
			copy(txt[ti:], mst)
			ti += mstsz
			copy(txt[ti:], bo[i:ci])
			ti += fsz
			copy(txt[ti:], med)
			ti += medsz
			copy(txt[ti:], bo[ci:cied])
			matches = append(matches, FileSearchMatch{Reg: reg, Text: txt})
			cnt++
		}
		ln++
	}
	if err := scan.Err(); err != nil {
		log.Printf("gide.FileSearch error: %v\n", err)
	}
	return cnt, matches
}

// FileNodeFlags define bitflags for FileNode state -- these extend ki.Flags
// and storage is an int64
type FileNodeFlags int64

const (
	// FileNodeOpen means file is open -- for directories, this means that
	// sub-files should be / have been loaded -- for files, means that they
	// have been opened e.g., for editing
	FileNodeOpen FileNodeFlags = FileNodeFlags(ki.FlagsN) + iota

	// FileNodeSymLink indicates that file is a symbolic link -- file info is
	// all for the target of the symlink
	FileNodeSymLink

	FileNodeFlagsN
)

//go:generate stringer -type=FileNodeFlags

var KiT_FileNodeFlags = kit.Enums.AddEnum(FileNodeFlagsN, true, nil) // true = bitflags

var FileNodeProps = ki.Props{
	"CallMethods": ki.PropSlice{
		{"RenameFile", ki.Props{
			"label": "Rename...",
			"desc":  "Rename file to new file name",
			"Args": ki.PropSlice{
				{"New Name", ki.Props{
					"width":         60,
					"default-field": "Nm",
				}},
			},
		}},
		{"NewFile", ki.Props{
			"label": "New File...",
			"desc":  "Create a new file in this folder",
			"Args": ki.PropSlice{
				{"File Name", ki.Props{
					"width": 60,
				}},
			},
		}},
		{"NewFolder", ki.Props{
			"label": "New Folder...",
			"desc":  "Create a new folder within this folder",
			"Args": ki.PropSlice{
				{"Folder Name", ki.Props{
					"width": 60,
				}},
			},
		}},
	},
}

//////////////////////////////////////////////////////////////////////////////
//    OpenDirMap

// OpenDirMap is a map for encoding directories that are open in the file
// tree.  The strings are typically relative paths.  The bool value is used to
// mark active paths and inactive (unmarked) ones can be removed.
type OpenDirMap map[string]bool

// Init initializes the map
func (dm *OpenDirMap) Init() {
	if *dm == nil {
		*dm = make(OpenDirMap, 1000)
	}
}

// IsOpen returns true if path is listed on the open map
func (dm *OpenDirMap) IsOpen(path string) bool {
	dm.Init()
	if _, ok := (*dm)[path]; ok {
		(*dm)[path] = true // mark
		return true
	}
	return false
}

// SetOpen adds the given path to the open map
func (dm *OpenDirMap) SetOpen(path string) {
	dm.Init()
	(*dm)[path] = true
}

// SetClosed removes given path from the open map
func (dm *OpenDirMap) SetClosed(path string) {
	dm.Init()
	delete(*dm, path)
}

// ClearFlags sets all the bool flags to false -- do this prior to traversing
// full set of active paths -- can then call RemoveStale to get rid of unused paths
func (dm *OpenDirMap) ClearFlags() {
	dm.Init()
	for key, _ := range *dm {
		(*dm)[key] = false
	}
}

// RemoveStale removes all entries with a bool = false value indicating that
// they have not been accessed since ClearFlags was called.
func (dm *OpenDirMap) RemoveStale() {
	dm.Init()
	for key, val := range *dm {
		if !val {
			delete(*dm, key)
		}
	}
}

//////////////////////////////////////////////////////////////////////////////
//    FileTreeView

// FileTreeView is a TreeView that knows how to operate on FileNode nodes
type FileTreeView struct {
	TreeView
}

var KiT_FileTreeView = kit.Types.AddType(&FileTreeView{}, nil)

func init() {
	kit.Types.SetProps(KiT_FileTreeView, FileTreeViewProps)
}

// FileNode returns the SrcNode as a FileNode
func (ft *FileTreeView) FileNode() *FileNode {
	if ft.This() == nil {
		return nil
	}
	fn := ft.SrcNode.Ptr.Embed(KiT_FileNode).(*FileNode)
	return fn
}

// DuplicateFiles calls DuplicateFile on any selected nodes
func (ft *FileTreeView) DuplicateFiles() {
	sels := ft.SelectedViews()
	for i := len(sels) - 1; i >= 0; i-- {
		sn := sels[i]
		ftv := sn.Embed(KiT_FileTreeView).(*FileTreeView)
		fn := ftv.FileNode()
		if fn != nil {
			fn.DuplicateFile()
		}
	}
}

// DeleteFiles calls DeleteFile on any selected nodes
func (ft *FileTreeView) DeleteFiles() {
	sels := ft.SelectedViews()
	for i := len(sels) - 1; i >= 0; i-- {
		sn := sels[i]
		ftv := sn.Embed(KiT_FileTreeView).(*FileTreeView)
		fn := ftv.FileNode()
		if fn != nil {
			fn.DeleteFile()
		}
	}
}

// RenameFiles calls RenameFile on any selected nodes
func (ft *FileTreeView) RenameFiles() {
	sels := ft.SelectedViews()
	for i := len(sels) - 1; i >= 0; i-- {
		sn := sels[i]
		ftv := sn.Embed(KiT_FileTreeView).(*FileTreeView)
		fn := ftv.FileNode()
		if fn != nil {
			CallMethod(fn, "RenameFile", ft.Viewport)
		}
	}
}

// OpenDirs
func (ft *FileTreeView) OpenDirs() {
	sels := ft.SelectedViews()
	for i := len(sels) - 1; i >= 0; i-- {
		sn := sels[i]
		ftv := sn.Embed(KiT_FileTreeView).(*FileTreeView)
		fn := ftv.FileNode()
		if fn != nil {
			fn.OpenDir()
		}
	}
}

// NewFile makes a new file in given selected directory node
func (ft *FileTreeView) NewFile(filename string) {
	sels := ft.SelectedViews()
	sz := len(sels)
	if sz == 0 { // shouldn't happen
		return
	}
	sn := sels[sz-1]
	ftv := sn.Embed(KiT_FileTreeView).(*FileTreeView)
	fn := ftv.FileNode()
	if fn != nil {
		fn.NewFile(filename)
	}
}

// NewFolder makes a new file in given selected directory node
func (ft *FileTreeView) NewFolder(foldername string) {
	sels := ft.SelectedViews()
	sz := len(sels)
	if sz == 0 { // shouldn't happen
		return
	}
	sn := sels[sz-1]
	ftv := sn.Embed(KiT_FileTreeView).(*FileTreeView)
	fn := ftv.FileNode()
	if fn != nil {
		fn.NewFolder(foldername)
	}
}

// Cut copies to clip.Board and deletes selected items
// satisfies gi.Clipper interface and can be overridden by subtypes
func (ft *FileTreeView) Cut() {
	if ft.IsRootOrField("Cut") {
		return
	}
	ft.Copy(false)
	// todo: in the future, move files somewhere temporary, then use those temps for paste..
	gi.PromptDialog(ft.Viewport, gi.DlgOpts{Title: "Cut Not Supported", Prompt: "File names were copied to clipboard and can be pasted to copy elsewhere, but files are not deleted because contents of files are not placed on the clipboard and thus cannot be pasted as such.  Use Delete to delete files."}, true, false, nil, nil)
}

// Paste pastes clipboard at given node
// satisfies gi.Clipper interface and can be overridden by subtypes
func (ft *FileTreeView) Paste() {
	md := oswin.TheApp.ClipBoard(ft.Viewport.Win.OSWin).Read([]string{mimedata.TextPlain})
	if md != nil {
		ft.PasteMime(md)
	}
}

// Drop pops up a menu to determine what specifically to do with dropped items
// satisfies gi.DragNDropper interface and can be overridden by subtypes
func (ft *FileTreeView) Drop(md mimedata.Mimes, mod dnd.DropMods) {
	ft.PasteMime(md)
	ft.DragNDropFinalize(mod)
}

// PasteMime applies a paste / drop of mime data onto this node
// always does a copy of files into / onto target
func (ft *FileTreeView) PasteMime(md mimedata.Mimes) {
	sroot := ft.RootView.SrcNode.Ptr
	tfn := ft.FileNode()
	if tfn == nil {
		return
	}
	if !tfn.IsDir() {
		if len(md) != 2 {
			gi.PromptDialog(ft.Viewport, gi.DlgOpts{Title: "Can Only Copy 1 File", Prompt: fmt.Sprintf("Only one file can be copied target file: %v -- currently have: %v", tfn.Name(), len(md)/2)}, true, false, nil, nil)
			return
		}
	}
	for _, d := range md {
		if d.Type != mimedata.TextPlain {
			continue
		}
		// todo: process file:/// kinds of paths..
		path := string(d.Data)
		sfni, ok := sroot.FindPathUnique(path)
		if !ok {
			fmt.Printf("giv.FileTreeView: could not find filenode at path: %v\n", path)
			continue
		}
		sfn := sfni.Embed(KiT_FileNode).(*FileNode)
		if sfn == nil {
			continue
		}
		if tfn.IsDir() {
			tfn.CopyFileToDir(string(sfn.FPath), sfn.Info.Mode)
		} else {
			tfn.CopyFileToFile(string(sfn.FPath), sfn.Info.Mode)
		}
	}
	tfn.UpdateNode()
}

// Dragged is called after target accepts the drop -- we just remove
// elements that were moved
// satisfies gi.DragNDropper interface and can be overridden by subtypes
func (ft *FileTreeView) Dragged(de *dnd.Event) {
	// fmt.Printf("ft dragged: %v\n", ft.PathUnique())
	if de.Mod != dnd.DropMove {
		return
	}
	sroot := ft.RootView.SrcNode.Ptr
	tfn := ft.FileNode()
	if tfn == nil {
		return
	}
	md := de.Data
	for _, d := range md {
		if d.Type != mimedata.TextPlain {
			continue
		}
		path := string(d.Data)
		sfni, ok := sroot.FindPathUnique(path)
		if !ok {
			fmt.Printf("giv.FileTreeView: could not find filenode at path: %v\n", path)
			continue
		}
		sfn := sfni.Embed(KiT_FileNode).(*FileNode)
		if sfn == nil {
			continue
		}
		// fmt.Printf("deleting: %v  path: %v\n", sfn.PathUnique(), sfn.FPath)
		sfn.DeleteFile()
	}
}

// FileTreeInactiveDirFunc is an ActionUpdateFunc that inactivates action if node is a dir
var FileTreeInactiveDirFunc = ActionUpdateFunc(func(fni interface{}, act *gi.Action) {
	ft := fni.(ki.Ki).Embed(KiT_FileTreeView).(*FileTreeView)
	fn := ft.FileNode()
	if fn != nil {
		act.SetInactiveState(fn.IsDir())
	}
})

// FileTreeActiveDirFunc is an ActionUpdateFunc that activates action if node is a dir
var FileTreeActiveDirFunc = ActionUpdateFunc(func(fni interface{}, act *gi.Action) {
	ft := fni.(ki.Ki).Embed(KiT_FileTreeView).(*FileTreeView)
	fn := ft.FileNode()
	if fn != nil {
		act.SetActiveState(fn.IsDir())
	}
})

var FileTreeViewProps = ki.Props{
	"indent":           units.NewValue(2, units.Ch),
	"spacing":          units.NewValue(.5, units.Ch),
	"border-width":     units.NewValue(0, units.Px),
	"border-radius":    units.NewValue(0, units.Px),
	"padding":          units.NewValue(0, units.Px),
	"margin":           units.NewValue(1, units.Px),
	"text-align":       gi.AlignLeft,
	"vertical-align":   gi.AlignTop,
	"color":            &gi.Prefs.Colors.Font,
	"background-color": "inherit",
	".exec": ki.Props{
		"font-weight": gi.WeightBold,
	},
	".open": ki.Props{
		"font-style": gi.FontItalic,
	},
	"#icon": ki.Props{
		"width":   units.NewValue(1, units.Em),
		"height":  units.NewValue(1, units.Em),
		"margin":  units.NewValue(0, units.Px),
		"padding": units.NewValue(0, units.Px),
		"fill":    &gi.Prefs.Colors.Icon,
		"stroke":  &gi.Prefs.Colors.Font,
	},
	"#branch": ki.Props{
		"icon":             "widget-wedge-down",
		"icon-off":         "widget-wedge-right",
		"margin":           units.NewValue(0, units.Px),
		"padding":          units.NewValue(0, units.Px),
		"background-color": color.Transparent,
		"max-width":        units.NewValue(.8, units.Em),
		"max-height":       units.NewValue(.8, units.Em),
	},
	"#space": ki.Props{
		"width": units.NewValue(.5, units.Em),
	},
	"#label": ki.Props{
		"margin":    units.NewValue(0, units.Px),
		"padding":   units.NewValue(0, units.Px),
		"min-width": units.NewValue(16, units.Ch),
	},
	"#menu": ki.Props{
		"indicator": "none",
	},
	TreeViewSelectors[TreeViewActive]: ki.Props{},
	TreeViewSelectors[TreeViewSel]: ki.Props{
		"background-color": &gi.Prefs.Colors.Select,
	},
	TreeViewSelectors[TreeViewFocus]: ki.Props{
		"background-color": &gi.Prefs.Colors.Control,
	},
	"CtxtMenuActive": ki.PropSlice{
		{"DuplicateFiles", ki.Props{
			"label":    "Duplicate",
			"updtfunc": FileTreeInactiveDirFunc,
		}},
		{"DeleteFiles", ki.Props{
			"label":    "Delete",
			"desc":     "Ok to delete file(s)?  This is not undoable and is not moving to trash / recycle bin",
			"confirm":  true,
			"updtfunc": FileTreeInactiveDirFunc,
		}},
		{"RenameFiles", ki.Props{
			"label": "Rename",
			"desc":  "Rename file to new file name",
		}},
		{"sep-open", ki.BlankProp{}},
		{"OpenDirs", ki.Props{
			"label":    "Open Dir",
			"desc":     "open given folder to see files within",
			"updtfunc": FileTreeActiveDirFunc,
		}},
		{"NewFile", ki.Props{
			"label":    "New File...",
			"desc":     "make a new file in this folder",
			"updtfunc": FileTreeActiveDirFunc,
			"Args": ki.PropSlice{
				{"File Name", ki.Props{
					"width": 60,
				}},
			},
		}},
		{"NewFolder", ki.Props{
			"label":    "New Folder...",
			"desc":     "make a new folder within this folder",
			"updtfunc": FileTreeActiveDirFunc,
			"Args": ki.PropSlice{
				{"Folder Name", ki.Props{
					"width": 60,
				}},
			},
		}},
	},
}

var fnFolderProps = ki.Props{
	"icon":     "folder-open",
	"icon-off": "folder",
}

func (ft *FileTreeView) Style2D() {
	fn := ft.FileNode()
	if fn != nil {
		if fn.IsDir() {
			if fn.HasChildren() {
				ft.Icon = gi.IconName("")
			} else {
				ft.Icon = gi.IconName("folder")
			}
			ft.SetProp("#branch", fnFolderProps)
			ft.Class = "folder"
		} else {
			ft.Icon = fn.Info.Ic
			if ft.Icon == "" || ft.Icon == "none" {
				ft.Icon = "blank"
			}
			if fn.IsExec() {
				ft.Class = "exec"
			} else if fn.IsOpen() {
				ft.Class = "open"
			} else {
				ft.Class = ""
			}
		}
	}
	ft.StyleTreeView()
	ft.LayData.SetFromStyle(&ft.Sty.Layout) // also does reset
}
