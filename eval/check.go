package eval

import (
	"github.com/xiaq/elvish/parse"
	"github.com/xiaq/elvish/util"
)

// Checker performs static checking on an Elvish AST. It also annotates the AST
// with static information that is useful during evaluation.
type Checker struct {
	name, text string
	scopes     []map[string]Type
	enclosed   map[string]Type
}

func NewChecker() *Checker {
	return &Checker{}
}

func (ch *Checker) startCheck(name, text string, scope map[string]Type) {
	ch.name = name
	ch.text = text
	ch.scopes = []map[string]Type{scope}
	ch.enclosed = make(map[string]Type)
}

func (ch *Checker) Check(name, text string, n *parse.ChunkNode, scope map[string]Type) (err error) {
	ch.startCheck(name, text, scope)
	defer util.Recover(&err)
	ch.checkChunk(n)
	return nil
}

func (ch *Checker) pushScope() {
	ch.scopes = append(ch.scopes, make(map[string]Type))
}

func (ch *Checker) popScope() {
	ch.scopes[len(ch.scopes)-1] = nil
	ch.scopes = ch.scopes[:len(ch.scopes)-1]
}

func (ch *Checker) pushVar(name string, t Type) {
	ch.scopes[len(ch.scopes)-1][name] = t
}

func (ch *Checker) popVar(name string) {
	delete(ch.scopes[len(ch.scopes)-1], name)
}

func (ch *Checker) hasVarOnThisScope(name string) bool {
	_, ok := ch.scopes[len(ch.scopes)-1][name]
	return ok
}

func (ch *Checker) errorf(n parse.Node, format string, args ...interface{}) {
	util.Panic(util.NewContextualError(ch.name, ch.text, int(n.Position()), format, args...))
}

// checkChunk checks a ChunkNode by checking all pipelines it contains.
func (ch *Checker) checkChunk(cn *parse.ChunkNode) {
	for _, pn := range cn.Nodes {
		ch.checkPipeline(pn)
	}
}

// checkClosure checks a ClosureNode by checking the chunk it contains.
// TODO(xiaq): Check that all pipelines have coherent IO ports.
func (ch *Checker) checkClosure(cn *parse.ClosureNode) *closureAnnotation {
	ch.pushScope()
	annotation := &closureAnnotation{}
	cn.Annotation = annotation

	bounds := [2]StreamType{}
	for _, pn := range cn.Chunk.Nodes {
		annotation := ch.checkPipeline(pn)
		var ok bool
		bounds[0], ok = bounds[0].commonType(annotation.bounds[0])
		if !ok {
			ch.errorf(pn, "Pipeline input stream incompatible with previous ones")
		}
		bounds[1], ok = bounds[1].commonType(annotation.bounds[1])
		if !ok {
			ch.errorf(pn, "Pipeline output stream incompatible with previous ones")
		}
	}
	annotation.bounds = bounds

	annotation.enclosed = ch.enclosed
	ch.enclosed = make(map[string]Type)
	ch.popScope()
	return annotation
}

// checkPipeline checks a PipelineNode by checking all forms and checking that
// all connected ports are compatible. It also annotates the node.
func (ch *Checker) checkPipeline(pn *parse.PipelineNode) *pipelineAnnotation {
	annotation := &pipelineAnnotation{}
	pn.Annotation = annotation

	var lastOutput StreamType
	for i, fn := range pn.Nodes {
		ch.checkForm(fn)
		a := fn.Annotation.(*formAnnotation)
		input := a.streamTypes[0]
		if i == 0 {
			annotation.bounds[0] = input
		} else {
			internal, ok := lastOutput.commonType(input)
			if !ok {
				ch.errorf(fn, "Form input type %v insatisfiable - previous form output is type %v", input, lastOutput)
			}
			annotation.internals = append(annotation.internals, internal)
		}
		lastOutput = a.streamTypes[1]
	}
	annotation.bounds[1] = lastOutput
	return annotation
}

func (ch *Checker) resolveVar(name string, n *parse.FactorNode) Type {
	if t := ch.tryResolveVar(name); t != nil {
		return t
	}
	ch.errorf(n, "undefined variable $%q", name)
	return nil
}

func (ch *Checker) tryResolveVar(name string) Type {
	thisScope := len(ch.scopes) - 1
	for i := thisScope; i >= 0; i-- {
		if t := ch.scopes[i][name]; t != nil {
			if i < thisScope {
				ch.enclosed[name] = t
			}
			return t
		}
	}
	return nil
}

func (ch *Checker) resolveCommand(name string, fa *formAnnotation) {
	if ct, ok := ch.tryResolveVar("fn-" + name).(*ClosureType); ok {
		// Defined function
		fa.commandType = commandDefinedFunction
		fa.streamTypes = ct.Bounds
	} else if bi, ok := builtinSpecials[name]; ok {
		// Builtin special
		fa.commandType = commandBuiltinSpecial
		fa.streamTypes = bi.streamTypes
		fa.builtinSpecial = &bi
	} else if bi, ok := builtinFuncs[name]; ok {
		// Builtin func
		fa.commandType = commandBuiltinFunction
		fa.streamTypes = bi.streamTypes
		fa.builtinFunc = &bi
	} else {
		// External command
		fa.commandType = commandExternal
		fa.streamTypes = [2]StreamType{fdStream, fdStream}
	}
}

// checkForm checks a FormNode by resolving the command statically and checking
// all terms. Special forms are then processed case by case. It also annotates
// the node.
func (ch *Checker) checkForm(fn *parse.FormNode) {
	// TODO(xiaq): Allow more interesting terms to be used as commands
	msg := "command must be a string or closure"
	if len(fn.Command.Nodes) != 1 {
		ch.errorf(fn.Command, msg)
	}
	command := fn.Command.Nodes[0]
	ch.checkFactor(command)

	annotation := &formAnnotation{}
	fn.Annotation = annotation
	switch command.Typ {
	case parse.StringFactor:
		ch.resolveCommand(command.Node.(*parse.StringNode).Text, annotation)
	case parse.ClosureFactor:
		ca := command.Node.(*parse.ClosureNode).Annotation.(*closureAnnotation)
		annotation.streamTypes = ca.bounds
	default:
		ch.errorf(fn.Command, msg)
	}

	for _, rd := range fn.Redirs {
		fd := rd.Fd()
		if fd > 1 {
			ch.errorf(rd, "redir on fd > 1 not yet supported")
		}
		switch rd := rd.(type) {
		case *parse.FdRedir:
			if annotation.streamTypes[fd] == chanStream {
				ch.errorf(rd, "fd redir on channel port")
			}
			if rd.OldFd > 1 {
				ch.errorf(rd, "fd redir from fd > 1 not yet supported")
			}
		case *parse.FilenameRedir:
			if annotation.streamTypes[fd] == chanStream {
				ch.errorf(rd, "filename redir on channel port")
			}
		}
		annotation.streamTypes[fd] = unusedStream
	}

	if annotation.commandType == commandBuiltinSpecial {
		annotation.specialAnnotation = annotation.builtinSpecial.check(ch, fn)
	} else {
		ch.checkTermList(fn.Args)
	}
}

func (ch *Checker) checkTerms(tns []*parse.TermNode) {
	for _, tn := range tns {
		ch.checkTerm(tn)
	}
}

// checkTermList checks a TermListNode by checking all terms it contains.
func (ch *Checker) checkTermList(ln *parse.TermListNode) {
	ch.checkTerms(ln.Nodes)
}

// checkTerm checks a TermNode by checking all factors it contains.
func (ch *Checker) checkTerm(tn *parse.TermNode) {
	for _, fn := range tn.Nodes {
		ch.checkFactor(fn)
	}
}

// checkFactor checks a FactorNode by analyzing different factor types case by
// case. A StringFactor is not checked at all. A VariableFactor is resolved
// statically. The other composite factor types are checked recursively.
func (ch *Checker) checkFactor(fn *parse.FactorNode) {
	switch fn.Typ {
	case parse.StringFactor:
	case parse.VariableFactor:
		ch.resolveVar(fn.Node.(*parse.StringNode).Text, fn)
	case parse.TableFactor:
		table := fn.Node.(*parse.TableNode)
		for _, tn := range table.List {
			ch.checkTerm(tn)
		}
		for _, tp := range table.Dict {
			ch.checkTerm(tp.Key)
			ch.checkTerm(tp.Value)
		}
	case parse.ClosureFactor:
		ca := ch.checkClosure(fn.Node.(*parse.ClosureNode))
		for name, typ := range ca.enclosed {
			if !ch.hasVarOnThisScope(name) {
				ch.enclosed[name] = typ
			}
		}
	case parse.ListFactor:
		ch.checkTermList(fn.Node.(*parse.TermListNode))
	case parse.OutputCaptureFactor, parse.StatusCaptureFactor:
		ch.checkPipeline(fn.Node.(*parse.PipelineNode))
	}
}
