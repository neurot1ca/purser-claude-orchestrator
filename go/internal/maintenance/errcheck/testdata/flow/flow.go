package flow

func Overwrite() int {
	x := 1
	x = 2
	return x
}
func Branch(b bool) int {
	x := 1
	if b {
		return x
	}
	x = 2
	return x
}
func Protected() int {
	x := 1
	p := &x
	x = 2
	y := 1
	f := func() int { return y }
	y = 2
	return *p + f()
}
func Loop() int {
	x := 1
	for i := 0; i < 2; i++ {
		x = i
	}
	return x
}
func Switch(b int) int {
	x := 1
	switch b {
	case 1:
		_ = x
		break
	default:
		x = 2
	}
	return x
}
func Updates() int {
	x := 1
	x++
	x += 2
	x = 3
	return x
}
func dead() {}

const deadConst = 1

func testOnly() {}

type thing struct{}

func (thing) method() {}
func Named() (x int)  { x = 1; x = 2; return }
func Labels() int {
	x := 1
	goto done
done:
	x = 2
	return x
}

func AliasedArray() int {
	x := [1]int{1}
	alias := x[:]
	x = [1]int{2}
	return alias[0]
}

type Mutable struct{ Value int }

func (m *Mutable) Remember() func() int { return func() int { return m.Value } }
func ImplicitAddress() int {
	x := Mutable{1}
	read := x.Remember()
	x = Mutable{2}
	return read()
}
func BranchJoin(b bool) int {
	x := 1
	if b {
		_ = x
	}
	x = 2
	return x
}
func DeadSwitch(b int) int {
	x := 7
	switch b {
	case 1:
		x = 2
		break
	default:
		x = 3
	}
	return x
}

type deadType struct{}

var deadVar = 1

func TypeSwitch(v any) int {
	x := 3
	switch tag := v; y := tag.(type) {
	case int:
		x = 1
		x = 2
		Use(x)
		y = 10
		y = 20
		_ = y
		return x
	default:
		return x
	}
}
func ZeroInitializers() {
	var s string = ""
	s = "used"
	_ = s
	b := false
	b = true
	_ = b
	var p *int = nil
	p = new(int)
	_ = p
	n := 0
	n = 0
	n = 1
	_ = n
	z := struct{ V int }{}
	z = struct{ V int }{1}
	_ = z
	a := [1]int{}
	a = [1]int{1}
	_ = a
	r := any(nil)
	r = 1
	_ = r
	var c complex128 = 0
	c = 1
	_ = c
	q := any(0)
	q = any(1)
	_ = q
}

func Use(x int)        {}
func Pair() (int, int) { return 1, 2 }
func TupleResult() int {
	x, y := Pair()
	Use(x)
	Use(y)
	x, y = Pair()
	return y
}
