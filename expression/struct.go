package expression

import "github.com/expr-lang/expr/vm"

type Expressions struct {
	Ignores []*vm.Program
	Removes []*vm.Program
	Labels  []*LabelExpression
}

type LabelExpression struct {
	Name    string
	Updates []*vm.Program
}
