package main

import (
	"fmt"
	"os"
	"os/exec"
	"debug/elf"
	"go/token"
	"go/parser"
	"go/ast"
	"go/printer"
	"bytes"
	"strings"
	"debug/dwarf"
	"sort"
	"io"
)

// if onlyStmt only check is_stmt instructions
const onlyStmt = false

func must(err error) {
	if err != nil {
		panic(err)
	}
}

type Func struct {
	Name string
	startLine, endLine int
}

type FuncRange struct {
	Rng [2]uint64
	Fn *Func
}

func main() {
	for _, arg := range os.Args[1:] {
		fmt.Printf("%s\n", arg)
		
		funcs := make(map[string]*Func)
		
		getLineRanges(arg, funcs)
		
		file := build(arg)
		if file == nil {
			// couldn't build?
			continue
		}
		
		dw, err := file.DWARF()
		must(err)
		
		funcRanges := getPCRanges(dw, funcs)
		checkLines(dw, funcs, funcRanges)
		
		file.Close()
	}
}

func getLineRanges(path string, funcs map[string]*Func) {
	var fset token.FileSet
	file, err := parser.ParseFile(&fset, path, nil, 0)
	must(err)
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncDecl:
			s := fset.Position(n.Pos())
			e := fset.Position(n.End())
			name := n.Name.Name
			if n.Recv != nil {
				name = "(" + withoutTypeParams(exprToString(n.Recv.List[0].Type)) + ")." + name
			}
			funcs["main." + name] = &Func{ Name: "main." + name, startLine: s.Line, endLine: e.Line }
			return false
		default:
			return true
		// TODO: function literals
		}
	})
}

func exprToString(t ast.Expr) string {
	var buf bytes.Buffer
	printer.Fprint(&buf, token.NewFileSet(), t)
	return buf.String()
}

func withoutTypeParams(in string) string {
	i := strings.Index(in, "[")
	j := strings.LastIndex(in, "]")
	if i >= 0 && j >= 0 && j > i {
		return in[:i] + in[j+1:]
	}
	return in
}

func getPCRanges(dw *dwarf.Data, funcs map[string]*Func) []FuncRange {
	r := []FuncRange{}
	
	rdr := dw.Reader()
	
	for {
		e, err := rdr.Next()
		if err != nil {
			must(err)
			break
		}
		if e == nil {
			break
		}
		if e.Tag != dwarf.TagSubprogram {
			continue
		}

		name, okname := e.Val(dwarf.AttrName).(string)
		low, oklow := e.Val(dwarf.AttrLowpc).(uint64)
		high, okhigh := e.Val(dwarf.AttrHighpc).(uint64)
		if !okname || !oklow || !okhigh {
			continue
		}
		name = withoutTypeParams(name)
		fn := funcs[name]
		if fn == nil {
			continue
		}
		r = append(r, FuncRange{ [2]uint64{ low, high }, fn })
	}
	sort.Slice(r, func(i, j int) bool { return r[i].Rng[0] < r[j].Rng[0] })
	return r
	
}

func build(path string) *elf.File {
	const tgt = "/tmp/badlngenerics-test"
	out, err := exec.Command("go", "build", "-o", tgt, "-gcflags=-N -l", path).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error compiling: %s", string(out))
		os.Exit(1)
	}
	f, _ := elf.Open(tgt)
	// error is ignored, it is usually 'bad magic number' which just means it isn't an executable
	return f
}

func checkLines(dw *dwarf.Data, funcs map[string]*Func, funcRanges []FuncRange) {
	rdr := dw.Reader()
	
	for {
		e, err := rdr.Next()
		if err != nil {
			must(err)
			break
		}
		if e == nil {
			break
		}
		if e.Tag != dwarf.TagCompileUnit {
			continue
		}
		
		lnrdr, err := dw.LineReader(e)
		must(err)
		var lne dwarf.LineEntry
		for {
			err := lnrdr.Next(&lne)
			if err == io.EOF {
				break
			}
			must(err)
			if onlyStmt && !lne.IsStmt {
				continue
			}
			fn := getFunc(lne.Address, funcRanges)
			if fn == nil {
				continue
			}
			if lne.Line < fn.startLine || lne.Line > fn.endLine {
				fmt.Printf("%s:%d %#x %s\n", lne.File.Name, lne.Line, lne.Address, fn.Name)
			}
		}
	}
}

func getFunc(pc uint64, funcRanges []FuncRange) *Func {
	//TODO: inefficient
	for i := range funcRanges {
		if funcRanges[i].Rng[0] <= pc && pc < funcRanges[i].Rng[1] {
			return funcRanges[i].Fn
		}
	}
	return nil
}
