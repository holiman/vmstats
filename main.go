package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/wcharczuk/go-chart"
	"github.com/wcharczuk/go-chart/drawing"
	"io/ioutil"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	dir = flag.String("dir", "", "Directory of files")
)

type opMeter struct {
	Num  uint64        //`json:"Count"`
	Time time.Duration //`json:"ExecTime"`
}

func gasCost(op vm.OpCode, blnum *big.Int) uint64 {
	switch op {
	case vm.STOP:
		return 0
	case vm.ADD, vm.SUB, vm.LT, vm.GT, vm.SLT, vm.SGT, vm.EQ, vm.ISZERO, vm.AND, vm.OR, vm.XOR, vm.NOT,
		vm.BYTE: // vm.CALLDATALOAD also has memory expansion
		return vm.GasFastestStep
	case vm.MUL, vm.DIV, vm.SDIV, vm.MOD, vm.SMOD, vm.SIGNEXTEND:
		return vm.GasFastStep
	case vm.ADDMOD, vm.MULMOD, vm.JUMP:
		return vm.GasMidStep
	case vm.ADDRESS, vm.ORIGIN, vm.CALLER, vm.CALLVALUE, vm.CALLDATASIZE, vm.CODESIZE, vm.GASPRICE,
		vm.COINBASE, vm.TIMESTAMP, vm.NUMBER, vm.DIFFICULTY, vm.GASLIMIT, vm.POP, vm.PC, vm.MSIZE, vm.GAS:
		return vm.GasQuickStep
	case vm.BLOCKHASH:
		return vm.GasExtStep
	case vm.JUMPI:
		return vm.GasSlowStep
	case vm.JUMPDEST:
		return params.JumpdestGas

	}
	if op >= vm.PUSH1 && op <= vm.PUSH32 {
		return vm.GasFastestStep
	}

	if op >= vm.SWAP1 && op <= vm.SWAP16 {
		return vm.GasFastestStep
	}
	if op >= vm.DUP1 && op <= vm.DUP16 {
		return vm.GasFastestStep
	}

	var gt params.GasTable = params.GasTableHomestead

	if params.MainnetChainConfig.IsEIP150(blnum) {
		gt = params.GasTableEIP150
	}
	if params.MainnetChainConfig.IsEIP158(blnum) {
		gt = params.GasTableEIP158
	}
	if params.MainnetChainConfig.IsConstantinople(blnum) {
		gt = params.GasTableConstantinople
	}
	switch op {
	case vm.SLOAD:
		return gt.SLoad
	case vm.EXTCODESIZE:
		return gt.ExtcodeSize
	//case vm.EXTCODECOPY: -- cost depends on stack values
	//	return gt.ExtcodeCopy
	case vm.BALANCE:
		return gt.Balance
	case vm.EXTCODEHASH:
		return gt.ExtcodeHash
	case vm.SHL, vm.SHR, vm.SAR:
		if params.MainnetChainConfig.IsConstantinople(blnum) {
			return vm.GasFastestStep
		}
		return 0
	case vm.CALL:
		return gt.Calls
	}

	return 0
}

type dataPoint struct {
	op          vm.OpCode
	blockNumber *big.Int
	count       uint64
	execTime    time.Duration
}

func (dp *dataPoint) gas() uint64 {
	return gasCost(dp.op, dp.blockNumber)
}
func (dp *dataPoint) totalGas() uint64 {
	return dp.count * dp.gas()
}

func (dp *dataPoint) MilliSecondsPerMgas() float64 {
	// gas / nanos * 1 000 M = gas / s
	// (gas / 1000 000 ) / s = Mgas / s
	// (gas / 1M ) * 1000M / nanos = Mgas / s
	// (gas * 1000 ) / nanos = Mgas/s
	if dp.totalGas() == 0 {
		return float64(0)
	}
	return float64(1000*dp.execTime) / float64(1000*dp.totalGas())
}

func (dp *dataPoint) Sub(prev *dataPoint) *dataPoint {
	if prev == nil {
		return dp
	}
	return &dataPoint{
		blockNumber: dp.blockNumber,
		execTime:    dp.execTime - prev.execTime,
		count:       dp.count - prev.count,
		op:          dp.op,
	}
}

type statCollection struct {
	data map[int](map[vm.OpCode]*dataPoint)
}

func newStatCollection() statCollection {
	return statCollection{
		data: make(map[int](map[vm.OpCode]*dataPoint)),
	}
}
func (stats *statCollection) collect(blnum int, data []byte) error {

	var m [256]opMeter
	if err := json.Unmarshal(data, &m); err != nil {
		fmt.Printf("error: %v", err)
		return err
	}
	//fmt.Printf("OPCODE;GASCOST;COUNT;TOTALTIME;TOTALTIME;TOTALGAS;MGASPERNS\n")
	stats.data[blnum] = make(map[vm.OpCode]*dataPoint)
	for i := 0; i < 256; i++ {
		metric := m[i]
		op := vm.OpCode(i)
		dp := &dataPoint{
			op:          op,
			blockNumber: new(big.Int).SetUint64(uint64(blnum)),
			count:       metric.Num,
			execTime:    metric.Time,
		}
		stats.data[blnum][op] = dp
	}
	return nil
}

func (stats *statCollection) series(op vm.OpCode, fromBlock int, yFunc func(point *dataPoint) float64) ([]float64, []float64) {

	var (
		xseries []float64
		yseries []float64
	)
	var numbers []int
	for k := range stats.data {
		numbers = append(numbers, k)
	}
	sort.Ints(numbers)

	var prevBlock map[vm.OpCode]*dataPoint
	for _, number := range numbers {
		if number < fromBlock {
			continue
		}
		block := stats.data[number]
		if prevBlock != nil {
			dp := block[op]
			prevDp := prevBlock[op]
			modDp := dp.Sub(prevDp)
			// Only count it if it's been done more than 1000 times
			if modDp.count > 500 {
				yseries = append(yseries, yFunc(modDp))
				xseries = append(xseries, float64(number))

			}
		}
		prevBlock = block
	}
	return xseries, yseries
}

func (stats *statCollection) numbers() []int {
	var numbers []int
	for k := range stats.data {
		numbers = append(numbers, k)
	}
	sort.Ints(numbers)
	return numbers
}

// Construct a filter, which returns true if the any value in the given series is above the threshold
func minFilter(threshold float64) func([]float64) bool {

	return func(vals []float64) bool {
		for _, v := range vals {
			if v >= threshold {
				return true
			}
		}
		return false
	}
}

type filterFn func(vals []float64) bool

func plot(ops []vm.OpCode, stat statCollection, yFunc func(dp *dataPoint) float64, title, x, y, filename string) (string, error) {
	return plotFilter(ops, stat, yFunc, title, x, y, filename, nil, 0)
}
func plotFilter(ops []vm.OpCode, stat statCollection, yFunc func(dp *dataPoint) float64, title, x, y, filename string, filter filterFn, fromBlock int) (string, error) {
	showCount := len(ops) == 1
	annotations := chart.AnnotationSeries{
		Annotations: []chart.Value2{
			{XValue: 1920000.0, YValue: 0, Label: "DaoFork"},
			{XValue: 2463000.0, YValue: 0, Label: "EIP150/TW"},
			{XValue: 2675000.0, YValue: 0, Label: "EIP155/SD"},
			{XValue: 4370000.0, YValue: 0, Label: "Byzantium"},
			{XValue: 7280000.0, YValue: 0, Label: "Constantinople"},
		}}

	var series []chart.Series
	for _, op := range ops {
		xvals, yvals := stat.series(op, fromBlock, yFunc)

		if filter == nil || filter(yvals) {
			serie := chart.ContinuousSeries{
				XValues: xvals,
				YValues: yvals,
				Name:    op.String(),
			}
			series = append(series, serie)
			if showCount {
				// Show simple moving average
				smaSerie := chart.SMASeries{
					InnerSeries: serie,
					Style: chart.Style{
						Show:        true,
						StrokeColor: drawing.ColorBlack,
					},
					Name: fmt.Sprintf("Moving AVG %v", serie.Name),
				}
				series = append(series, smaSerie)
			}
			if showCount {
				secondaryYSeries, yvals := stat.series(op, fromBlock, func(dp *dataPoint) float64 {
					return float64(dp.count)
				})
				countSerie := chart.ContinuousSeries{
					XValues: secondaryYSeries,
					YValues: yvals,
					YAxis:   chart.YAxisSecondary,
					Style: chart.Style{
						StrokeColor: drawing.ColorRed,
						Show:        true,
					},
					Name: "Count",
				}
				series = append(series, countSerie)
			}
		}

	}
	series = append(series, annotations)

	graph := chart.Chart{
		Title:      fmt.Sprintf(title),
		TitleStyle: chart.StyleShow(),

		XAxis: chart.XAxis{
			Name:      x,
			NameStyle: chart.StyleShow(),
			Style:     chart.StyleShow(),
		},
		YAxis: chart.YAxis{
			Name:      y,
			NameStyle: chart.StyleShow(),
			Style:     chart.StyleShow(),
		},

		Series: series,
	}
	if showCount {
		graph.YAxisSecondary = chart.YAxis{
			Name:      "Count",
			NameStyle: chart.StyleShow(),
			Style:     chart.StyleShow(), //enables / displays the secondary y-axis
		}
	}

	graph.Elements = []chart.Renderable{
		chart.LegendLeft(&graph),
	}
	buffer := bytes.NewBuffer([]byte{})
	if err := graph.Render(chart.PNG, buffer); err != nil {
		return "", err
	}
	path := fmt.Sprintf("./charts/%s", filename)
	if err := ioutil.WriteFile(path, buffer.Bytes(), 0644); err != nil {
		return path, err
	}
	return path, nil
}

var RANGE0 = []vm.OpCode{
	vm.ADD,
	vm.MUL,
	vm.SUB,
	vm.DIV,
	vm.SDIV,
	vm.MOD,
	vm.SMOD,
	vm.ADDMOD,
	vm.MULMOD,
	vm.EXP,
	vm.SIGNEXTEND,
}
var RANGE1 = []vm.OpCode{
	vm.LT,
	vm.GT,
	vm.SLT,
	vm.SGT,
	vm.EQ,
	vm.ISZERO,
	vm.AND,
	vm.OR,
	vm.XOR,
	vm.NOT,
	vm.BYTE,
	//vm.SHL,
	//vm.SHR,
	//vm.SAR,
}
var RANGE2 = []vm.OpCode{
	vm.SHA3,
}
var RANGE3p1 = []vm.OpCode{
	vm.ADDRESS,
	vm.BALANCE,
	vm.ORIGIN,
	vm.CALLER,
	vm.CALLVALUE,
	vm.CALLDATASIZE,
}

var RANGE3p2 = []vm.OpCode{
	vm.CODESIZE,
	vm.GASPRICE,
	vm.EXTCODESIZE,
	vm.RETURNDATASIZE,
	vm.EXTCODEHASH,
	//vm.CALLDATALOAD,
	//vm.CALLDATACOPY,
	//vm.CODECOPY,
	//vm.EXTCODECOPY,
	//vm.RETURNDATACOPY,
}
var RANGE4 = []vm.OpCode{
	//vm.BLOCKHASH,
	vm.COINBASE,
	vm.TIMESTAMP,
	vm.NUMBER,
	vm.DIFFICULTY,
	vm.GASLIMIT,
}
var RANGE4p2 = []vm.OpCode{
	vm.BLOCKHASH,
}
var RANGE5p1 = []vm.OpCode{
	vm.POP,
	vm.MLOAD,
	vm.SLOAD,
	vm.PC,
	vm.MSIZE,
	vm.GAS,
}
var RANGE6 = []vm.OpCode{
	vm.PUSH1,
	vm.PUSH2,
	vm.PUSH3,
	vm.PUSH4,
	vm.PUSH5,
	vm.PUSH6,
	vm.PUSH7,
	vm.PUSH8,
	vm.PUSH9,
	vm.PUSH10,
	vm.PUSH11,
	vm.PUSH12,
	vm.PUSH13,
	vm.PUSH14,
	vm.PUSH15,
	vm.PUSH16,
	vm.PUSH17,
	vm.PUSH18,
	vm.PUSH19,
	vm.PUSH20,
	vm.PUSH21,
	vm.PUSH22,
	vm.PUSH23,
	vm.PUSH24,
	vm.PUSH25,
	vm.PUSH26,
	vm.PUSH27,
	vm.PUSH28,
	vm.PUSH29,
	vm.PUSH30,
	vm.PUSH31,
	vm.PUSH32,
	vm.DUP1,
	vm.DUP2,
	vm.DUP3,
	vm.DUP4,
	vm.DUP5,
	vm.DUP6,
	vm.DUP7,
	vm.DUP8,
	vm.DUP9,
	vm.DUP10,
	vm.DUP11,
	vm.DUP12,
	vm.DUP13,
	vm.DUP14,
	vm.DUP15,
	vm.DUP16,
	vm.SWAP1,
	vm.SWAP2,
	vm.SWAP3,
	vm.SWAP4,
	vm.SWAP5,
	vm.SWAP6,
	vm.SWAP7,
	vm.SWAP8,
	vm.SWAP9,
	vm.SWAP10,
	vm.SWAP11,
	vm.SWAP12,
	vm.SWAP13,
	vm.SWAP14,
	vm.SWAP15,
	vm.SWAP16,
}

var RANGE7 = []vm.OpCode{
	vm.LOG0,
	vm.LOG1,
	vm.LOG2,
	vm.LOG3,
	vm.LOG4,
}

var allOps []vm.OpCode

func init() {
	for i := 0; i < 0xff; i++ {
		allOps = append(allOps, vm.OpCode(i))
	}
}

func pie(filename string, stat statCollection, start, end int) error {
	timeGraph := chart.PieChart{
		Width:      600,
		Height:     800,
		Title:      fmt.Sprintf("Blocks %d to %d - Time spent", start, end),
		TitleStyle: chart.StyleShow(),
	}
	countGraph := chart.PieChart{
		Width:      600,
		Height:     800,
		Title:      fmt.Sprintf("Blocks %d to %d - Total count", start, end),
		TitleStyle: chart.StyleShow(),
	}
	// Get the aggregate from blocks 0 to end
	//blnums := stat.numbers()
	// Aggregate is in the last one
	//lastBlock := blnums[len(blnums) -1]

	lastStat := stat.data[end]
	firstStat := stat.data[start]
	var timeValues []chart.Value
	var countValues []chart.Value
	var zero = &dataPoint{}
	for op := vm.OpCode(0); op < 255; op++ {
		dpStart := firstStat[op]

		if dpStart == nil {
			dpStart = zero
		}
		dpEnd := lastStat[op]
		if dpEnd.count > 0 {
			timeValues = append(timeValues, chart.Value{
				Value: float64(dpEnd.execTime) - float64(dpStart.execTime),
				Label: op.String(),
			})
			countValues = append(countValues, chart.Value{
				Value: float64(dpEnd.count) - float64(dpStart.count),
				Label: op.String(),
			})
		}
	}
	timeGraph.Values = timeValues
	countGraph.Values = countValues

	buffer := bytes.NewBuffer([]byte{})
	if err := timeGraph.Render(chart.PNG, buffer); err != nil {
		return err
	}
	if err := ioutil.WriteFile(fmt.Sprintf("./charts/%s-time.png", filename), buffer.Bytes(), 0644); err != nil {
		return err
	}
	buffer = bytes.NewBuffer([]byte{})
	if err := countGraph.Render(chart.PNG, buffer); err != nil {
		return err
	}
	if err := ioutil.WriteFile(fmt.Sprintf("./charts/%s-count.png", filename), buffer.Bytes(), 0644); err != nil {
		return err
	}

	return nil

}

func barchart(filename, runinfo string, stat statCollection, start, end int) (string, error) {
	g := chart.BarChart{
		Width: 1000,
		//Title:      fmt.Sprintf("Blocks %d to %d - Time per gas (Top 25)\n %v (excluding < 1 exec per block)", start, end, runinfo),
		TitleStyle: chart.StyleShow(),
		XAxis: chart.Style{
			Show:                true,
			TextRotationDegrees: 90.0,
		},
		Background: chart.Style{
			Padding: chart.Box{
				Top:    40,
				Bottom: 80,
			},
		},
		BarWidth: 20,
		YAxis: chart.YAxis{
			Style: chart.StyleShow(),
		},
	}

	lastStat := stat.data[end]
	firstStat := stat.data[start]
	var vals []chart.Value

	var zero = &dataPoint{
		blockNumber:new(big.Int),

	}
	fmt.Printf("--------\n")
	for op := vm.OpCode(0); op < 255; op++ {
		dpStart := firstStat[op]

		if dpStart == nil {
			dpStart = zero
		}
		dpEnd := lastStat[op]
		if dpEnd == nil {
			return "", fmt.Errorf("data missing for %d", end)
		}
		if dpEnd.blockNumber == nil || dpStart.blockNumber == nil {
			continue
		}
		// exclude those that are executed less than once per
		nBlocks := dpEnd.blockNumber.Uint64() - dpStart.blockNumber.Uint64()
		nExecs := dpEnd.count - dpStart.count
		//fmt.Printf("nBlocks %d, nExecs %d\n", nBlocks, nExecs)
		if nBlocks > nExecs {
			continue
		}
		if dpEnd.count > 0 {
			modDp := dpEnd.Sub(dpStart)

			vals = append(vals, chart.Value{
				Value: modDp.MilliSecondsPerMgas(),
				Label: fmt.Sprintf("%v (%d)", op.String(), gasCost(op, modDp.blockNumber)),
			})
		}
	}
	sort.Slice(vals, func(i, j int) bool {
		return vals[i].Value > vals[j].Value
	})
	// Only use the top 25
	if len(vals) > 25 {
		vals = vals[:25]
	}
	g.Title = fmt.Sprintf("Blocks %d to %d - Time per gas (Top %d)\n %v (excluding < 1 exec per block)", start, end, len(vals), runinfo)

	g.Bars = vals

	buffer := bytes.NewBuffer([]byte{})
	if err := g.Render(chart.PNG, buffer); err != nil {
		return "", err
	}
	path := fmt.Sprintf("./charts/%s.png", filename)
	if err := ioutil.WriteFile(path, buffer.Bytes(), 0644); err != nil {
		return "", err
	}

	return path, nil

}

func main() {
	barcharts("./m5d.2xlarge.run3", "run3")
	barcharts("./m5d.2xlarge.run2", "run2")
	barcharts("./m5d.2xlarge", "run1")

}

func barcharts(dir, info string) {
	files, _ := ioutil.ReadDir(dir)

	stat := newStatCollection()
	for _, fStat := range files {
		if fStat.IsDir() {
			continue
		}
		if !strings.HasPrefix(fStat.Name(), "metrics_to") {
			continue
		}
		blockstring := strings.Split(fStat.Name(), "_")[2]
		blnum, _ := strconv.Atoi(blockstring)
		dat, err := ioutil.ReadFile(fmt.Sprintf("%s/%s", dir, fStat.Name()))
		if err != nil {
			fmt.Printf("error: %v", err)
			os.Exit(1)
		}
		stat.collect(blnum, dat)
	}
	for _, op := range []vm.OpCode{vm.BLOCKHASH, vm.SLOAD, vm.BALANCE} {

		fmt.Printf("Plotting %v\n", op)
		var timepergas = func(dp *dataPoint) float64 {
			return dp.MilliSecondsPerMgas()
		}

		fname := fmt.Sprintf("%v-%v.png", op, info)
		path, err := plot([]vm.OpCode{op}, stat, timepergas,
			fmt.Sprintf("Milliseconds per Mgas (%v) - %v", op, info),
			"Blocknumber", "Milliseconds", fname)
		if err != nil {
			fmt.Printf("Error %v", err)
		} else {
			fmt.Println(path)
		}
	}

	// And let's make some bar charts over the time per gas
	var barch = 0
	for ; barch < 7; barch++ {
		if file, err := barchart(fmt.Sprintf("%v.total-bars-%d", info, barch), info,
			stat, barch*1000000, (barch+1)*1000000); err != nil {
			fmt.Printf("Error: %v", err)
			break
			//syscall.Exit(1)
		} else {
			fmt.Println(file)
		}
	}

}

func firstRun() {

	dir := "./m5d.2xlarge"
	files, _ := ioutil.ReadDir(dir)

	stat := newStatCollection()
	for _, fStat := range files {
		if fStat.IsDir() {
			continue
		}
		if !strings.HasPrefix(fStat.Name(), "metrics_to") {
			continue
		}
		blockstring := strings.Split(fStat.Name(), "_")[2]
		blnum, _ := strconv.Atoi(blockstring)
		dat, err := ioutil.ReadFile(fmt.Sprintf("%s/%s", dir, fStat.Name()))
		if err != nil {
			fmt.Printf("error: %v", err)
			os.Exit(1)
		}
		stat.collect(blnum, dat)
	}

	var time = func(dp *dataPoint) float64 {
		return float64(dp.execTime) / 1000000
	}
	var timeCapped = func(dp *dataPoint) float64 {
		v := float64(dp.execTime) / 1000000
		if v < 100000 {
			return v
		}
		return 100000
	}

	// Let's make some donuts aswell
	var donut = 0
	for ; donut < 7; donut++ {
		if err := pie(fmt.Sprintf("total-pie-%d", donut),
			stat, donut*1000000, (donut+1)*1000000); err != nil {
			fmt.Printf("Error: %v", err)
			syscall.Exit(1)
		}
	}

	if _, err := plot(allOps, stat, time, "Time spent", "Blocknumber", "Milliseconds", "timespent.png"); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}
	if _, err := plotFilter(allOps, stat, timeCapped, "Time spent", "Blocknumber", "Milliseconds",
		"timespentCapped.png", minFilter(45000), 3220000); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	var timepergas = func(dp *dataPoint) float64 {
		return dp.MilliSecondsPerMgas()
	}

	var timepergasCapAt = func(cap float64) func(*dataPoint) float64 {
		return func(dp *dataPoint) float64 {
			if g := dp.MilliSecondsPerMgas(); g < cap {
				return g
			}
			return cap
		}
	}

	if _, err := plot(RANGE0, stat, timepergas,
		"Milliseconds per Mgas (0x00 opcodes - Arithmetic)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("arithmetics.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE0, stat, timepergasCapAt(250.0),
		"Milliseconds per Mgas (0x00 opcodes - Arithmetic) - capped", "Blocknumber", "Milliseconds",
		fmt.Sprintf("arithmetics_cap.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE1, stat, timepergasCapAt(250.0),
		"Milliseconds per Mgas (0x10 opcodes - Comparison)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("comparison_cap.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}
	if _, err := plot(RANGE2, stat, time,
		"Time spent on (0x30 opcodes - SHA3)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("sha3.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}
	if _, err := plot(RANGE3p1, stat, timepergasCapAt(500.0),
		"Milliseconds per Mgas (0x30 opcodes - Context, part 1)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("context1.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}
	if _, err := plot(RANGE3p2, stat, timepergasCapAt(500.0),
		"Milliseconds per Mgas (0x30 opcodes - Context, part 2)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("context2.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE4, stat, timepergasCapAt(600.0),
		"Milliseconds per Mgas (0x40 opcodes - Block ops)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("blockops_cap.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE4p2, stat, timepergasCapAt(3000.0),
		"Milliseconds per Mgas (BLOCKHASH)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("blockhash.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE5p1, stat, timepergasCapAt(3000.0),
		"Milliseconds per Mgas (0x50 Storage and execution - part 1)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("storage1.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}
	if _, err := plot(RANGE6, stat, timepergasCapAt(600.0),
		"Milliseconds per Mgas (0x60 Pops, Swaps, Dups)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("range60.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE6, stat, timepergasCapAt(100.0),
		"Milliseconds per Mgas (0x60 Pops, Swaps, Dups) - capped at 100", "Blocknumber", "Milliseconds",
		fmt.Sprintf("range60p2.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot(RANGE7, stat, time,
		"Time spent on log operations (0x70 LOG) ", "Blocknumber", "Milliseconds",
		fmt.Sprintf("logging.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if _, err := plot([]vm.OpCode{vm.SLOAD}, stat, timepergas,
		"Milliseconds per Mgas (SLOAD)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("sload.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}
	if _, err := plot([]vm.OpCode{vm.BALANCE}, stat, timepergas,
		"Milliseconds per Mgas (BALANCE)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("balance.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

}
