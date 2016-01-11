package mutest

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var nodeArray = make([]ast.Node, 0)
var successfulMutations = make([]ast.Node, 0)
var fset = token.NewFileSet()

func check(e error) {
	if e != nil {
		panic(e)
	}
}

// File is a wrapper for the state of a file used in the parser.
// The basic parse tree walker is a method of this type.
type File struct {
	fset      *token.FileSet
	name      string // Name of file.
	astFile   *ast.File
	atomicPkg string // Package name for "sync/atomic" in this file.
}

// Mutates the node, runs the test, then un-mutates the node
// Saves successful mutations to
func runTest(node ast.Node, fset *token.FileSet, file *ast.File, filename string, mutator Mutator) []byte {
	// Mutate the AST
	beforeOp, afterOp := mutator.Mutate(node)

	// Create new file
	genFile, err := os.Create(filename)
	check(err)
	defer genFile.Close()

	// Write AST to file
	printer.Fprint(genFile, fset, file)
	genFile.Sync()

	// Exec
	args := []string{"test"}
	cmd := exec.Command("go", args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		fmt.Println("Mutation did not cause a failure! From: ", beforeOp, " to ", afterOp, " pos: ", node.Pos())
	} else if _, ok := err.(*exec.ExitError); ok {
		lines := bytes.Split(output, []byte("\n"))
		lastLine := lines[len(lines) - 2]
		if !bytes.HasPrefix(lastLine, []byte("FAIL")) {
			fmt.Fprintf(os.Stderr, "mutation %s to %s tests resulted in an error: %s\n", beforeOp, afterOp, lastLine)
		} else {
			fmt.Println("mutation tests failed as expected! From", beforeOp, " to ", afterOp)
		}
	} else {
		fmt.Errorf("mutation failed to run tests: %s\n", err)
	}

	// Un-mutate AST
	mutator.Unmutate(node)

	// Remove file so next run will be clean
	err = os.Remove(filename)
	check(err)
	return output
}



type Mutator interface {
	Name() string
	Description() string
	Mutate(node ast.Node) (token.Token, token.Token)
	Unmutate(node ast.Node)
}

type SimpleMutator struct {}

func (*SimpleMutator) Name() string {
	return "SimpleMutator"
}

func (*SimpleMutator) Description() string {
	return "SimpleMutator mutates binary and negation statements"
}

// Mutates a given node (i.e. switches '==' to '!=')
func (*SimpleMutator) Mutate(node ast.Node) (token.Token, token.Token) {
	var beforeOp, afterOp token.Token
	switch n := node.(type) {
	case *ast.BinaryExpr:
		beforeOp = n.Op
		switch n.Op {
		case token.LAND:
			n.Op = token.LOR
		case token.LOR:
			n.Op = token.LAND
		case token.EQL:
			n.Op = token.NEQ
		case token.NEQ:
			n.Op = token.EQL
		case token.GEQ:
			n.Op = token.LSS
		case token.LEQ:
			n.Op = token.GTR
		case token.GTR:
			n.Op = token.LEQ
		case token.LSS:
			n.Op = token.GEQ
		default:
			panic(n.Op)
		}
		afterOp = n.Op
	case *ast.UnaryExpr:
		beforeOp = n.Op
		n.X = &ast.UnaryExpr{OpPos: n.OpPos, Op: token.NOT, X: n.X}
		afterOp = n.Op
	}
	return beforeOp, afterOp
}

func (m *SimpleMutator) Unmutate(node ast.Node) {
	m.Mutate(node)
}

func addSides(node ast.Expr) {
	switch n := node.(type) {
	case *ast.BinaryExpr:
		if n.Op == token.LAND || n.Op == token.LOR {
			addSides(n.X)
			addSides(n.Y)
		}
		nodeArray = append(nodeArray, node)
	case *ast.UnaryExpr:
		nodeArray = append(nodeArray, node)
	}
}

// Visit implements the ast.Visitor interface.
// Finds candidates for mutating and adds them to nodeArray
func (f *File) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.ForStmt:
		switch n := n.Cond.(type) {
		case *ast.BinaryExpr:
			if n.Op == token.LAND || n.Op == token.LOR {
				addSides(n)
			} else {
				nodeArray = append(nodeArray, n)
			}
		case *ast.UnaryExpr:
			nodeArray = append(nodeArray, n)
		}
	case *ast.IfStmt:
		switch n := n.Cond.(type) {
		case *ast.BinaryExpr:
			if n.Op == token.LAND || n.Op == token.LOR {
				addSides(n)
			}
			nodeArray = append(nodeArray, n)
		case *ast.UnaryExpr:
			if n.Op == token.NOT {
				nodeArray = append(nodeArray, n)
			}
		}
	/*	case *ast.AssignStmt:
			fmt.Println("ASSIGN statement: lhs: ", n.Lhs, " Tok: ", n.Tok, " rhs: ", n.Rhs)
		case *ast.ReturnStmt:
			fmt.Println("Return statement: return: ", n.Results)*/
	}
	return f
}

func doWork(codeFilePath, testFilePath string, mutator Mutator) [][]byte {
	codeFileParts := strings.Split(codeFilePath, "/")
	codeFilename := codeFileParts[len(codeFileParts) - 1]
	testFileParts := strings.Split(testFilePath, "/")
	testFilename := testFileParts[len(testFileParts) - 1]

	// Read in Test File
	dat, err := ioutil.ReadFile(testFilePath)
	check(err)

	// Read in and parse code file

	name := codeFilePath
	content, err := ioutil.ReadFile(name)
	check(err)
	parsedFile, err := parser.ParseFile(fset, name, content, 0)
	check(err)

	file := &File{
		fset:    fset,
		name:    name,
		astFile: parsedFile,
	}

	ast.Walk(file, file.astFile)
	//ast.Fprint(os.Stdout, fset, file.astFile, ast.NotNilFilter)
	//printer.Fprint(os.Stdout, fset, file.astFile)

	fmt.Println("*****************************************************")
	dir, err := os.Getwd()
	check(err)
	// Create a directory to test from
	genPath := filepath.Join(dir, "..", "generated_mutest")
	os.Mkdir(genPath, os.ModeDir | os.ModePerm)
	check(err)
	filename := filepath.Join(genPath, codeFilename)

	// Copy the test file into the new directory
	genTestFile, err := os.Create(filepath.Join(genPath, testFilename))
	check(err)
	defer genTestFile.Close()
	err = ioutil.WriteFile(filepath.Join(genPath, testFilename), dat, 0644)
	check(err)

	err = os.Chdir(genPath)
	check(err)

	output := make([][]byte, 0)

	for i := range nodeArray {
		output = append(output, runTest(nodeArray[i], fset, file.astFile, filename, mutator))
	}

	err = os.Chdir("../mutest")
	check(err)
	// Remove the created directory
	err = os.RemoveAll(genPath)
	check(err)
	nodeArray = make([]ast.Node, 0)
	return output
}

func main() {
	codeFilePathPtr := flag.String("c", "", "The path to the code file to mutate")
	testFilePathPtr := flag.String("t", "", "The path to the test file against which to test mutations")
	flag.Parse()
	mutator := &SimpleMutator{}
	doWork(*codeFilePathPtr, *testFilePathPtr, mutator)
}
