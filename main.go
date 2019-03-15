package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/wcharczuk/go-chart"
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
		vm.BYTE, vm.CALLDATALOAD:
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
	case vm.EXTCODECOPY:
		return gt.ExtcodeCopy
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

func (dp *dataPoint) mGasPerSec() float64 {
	// gas / nanos * 1 000 M = gas / s
	// (gas / 1000 000 ) / s = Mgas / s
	// (gas / 1M ) * 1000M / nanos = Mgas / s
	// (gas * 1000 ) / nanos = Mgas/s
	return float64(1000*dp.totalGas()) / float64(dp.execTime)
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

func (dp *dataPoint) String() string {

	return fmt.Sprintf("number %s, op %s; gas %d; count %d; execTime %d;execTime  %s; totalGas %d; MgasPerS %.03f",
		dp.blockNumber,
		dp.op.String(),
		dp.gas(),
		dp.count,
		dp.execTime,
		dp.execTime,
		dp.totalGas(),
		dp.mGasPerSec())
}
func (dp *dataPoint) CSV() string {

	return fmt.Sprintf("number;%s;opcode;%s;gas;%d;count;%d;time;%d;time;%s;totalgas;%d;mgaspers;%.03f",
		dp.blockNumber,
		dp.op.String(),
		dp.gas(),
		dp.count,
		dp.execTime,
		dp.execTime,
		dp.totalGas(),
		dp.mGasPerSec())
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


func (stats *statCollection) series(op vm.OpCode, yFunc func(point *dataPoint) float64) ([]float64, []float64) {

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
		block := stats.data[number]
		if prevBlock != nil {
			dp := block[op]
			prevDp := prevBlock[op]
			modDp := dp.Sub(prevDp)
			// Only count it if it's been done more than 1000 times
			if modDp.count > 1000{
				yseries = append(yseries, yFunc(modDp))
				xseries = append(xseries, float64(number))

			}
		}
		prevBlock = block
	}
	return xseries, yseries
}

func plot(ops []vm.OpCode, stat statCollection, yFunc func(dp *dataPoint) float64, title, x, y, filename string) error {
	var series []chart.Series
	for _, op := range ops {
		xvals, yvals := stat.series(op, yFunc)
		serie := chart.ContinuousSeries{
			XValues: xvals,
			YValues: yvals,
			Name:    op.String(),
		}
		smaSerie := chart.SMASeries{
			InnerSeries:serie,
			Name: serie.Name,
		}
		series = append(series, smaSerie)
	}

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
	graph.Elements = []chart.Renderable{
		chart.LegendLeft(&graph),
	}
	buffer := bytes.NewBuffer([]byte{})
	if err := graph.Render(chart.PNG, buffer); err != nil {
		return err
	}
	if err := ioutil.WriteFile(fmt.Sprintf("./charts/%s", filename), buffer.Bytes(), 0644); err != nil {
		return err
	}
	return nil
}

var mostOps = []vm.OpCode{
	vm.STOP, vm.ADD, vm.SUB, vm.LT, vm.GT, vm.SLT, vm.SGT,
	vm.EQ, vm.ISZERO, vm.AND, vm.OR, vm.XOR, vm.NOT, vm.BYTE, vm.CALLDATALOAD,
	vm.MUL,
	vm.DIV,
	vm.SDIV, vm.MOD, vm.SMOD, vm.SIGNEXTEND,
	vm.ADDMOD,
	vm.MULMOD, vm.JUMP, vm.ADDRESS, vm.ORIGIN, vm.CALLER, vm.CALLVALUE,
	vm.CALLDATASIZE, vm.CODESIZE, vm.GASPRICE, vm.COINBASE, vm.TIMESTAMP,
	vm.NUMBER, vm.DIFFICULTY, vm.GASLIMIT, vm.POP, vm.PC, vm.MSIZE, vm.GAS,
	vm.BLOCKHASH, vm.JUMPI, vm.JUMPDEST, vm.SLOAD, vm.EXTCODESIZE, vm.EXTCODECOPY,
	vm.BALANCE, vm.EXTCODEHASH, vm.SHL,vm.SSTORE,
	vm.SHR, vm.SAR, vm.CALL,
}

var someOps = []vm.OpCode{
	vm.PUSH1,vm.PUSH2,
	vm.PUSH32,
	vm.ADDMOD,
	vm.ADDRESS, vm.ORIGIN,
	vm.CALLER,
	vm.CALLVALUE,
	vm.CALLDATASIZE, vm.CODESIZE, vm.GASPRICE, vm.COINBASE, vm.TIMESTAMP,
	vm.NUMBER, vm.DIFFICULTY, vm.GASLIMIT, vm.POP, vm.PC, vm.MSIZE, vm.GAS,
	vm.BLOCKHASH,
	vm.JUMPI, vm.JUMPDEST,
	vm.SLOAD,
	vm.EXTCODESIZE,
	vm.BALANCE, vm.EXTCODEHASH, vm.SHL,
	vm.SHR, vm.SAR,
}

func main() {
	//dir := "./4760K_to_5040K"
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

	if err := plot(mostOps, stat, time, "Time spent", "Blocknumber", "Milliseconds",
		"timespent.png"); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	var timepergas = func(dp *dataPoint) float64 {
		return dp.MilliSecondsPerMgas()
	}
	var i = 0
	for i := 0; i + 5 < len(someOps); i+= 5{

		if err := plot(someOps[i:i+5], stat, timepergas,
			"Millseconds per Mgas", "Blocknumber", "Milliseconds",
			fmt.Sprintf("timepergas%d.png", i)); err != nil {
			fmt.Printf("Error: %v", err)
			syscall.Exit(1)
		}

	}
	if err := plot(someOps[i:i+5], stat, timepergas,
		"Millseconds per Mgas", "Blocknumber", "Milliseconds",
		fmt.Sprintf("timepergas%d.png", i)); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

	if err := plot([]vm.OpCode{vm.SLOAD}, stat, timepergas,
		"Millseconds per Mgas (SLOAD)", "Blocknumber", "Milliseconds",
		fmt.Sprintf("sload.png")); err != nil {
		fmt.Printf("Error: %v", err)
		syscall.Exit(1)
	}

}
