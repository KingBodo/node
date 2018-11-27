package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/BiJie/BinanceChain/common/client"
	"github.com/BiJie/BinanceChain/common/tx"
	"github.com/BiJie/BinanceChain/common/types"
	"github.com/BiJie/BinanceChain/plugins/dex"
	"github.com/BiJie/BinanceChain/plugins/dex/order"
	"github.com/BiJie/BinanceChain/plugins/tokens"
	"github.com/BiJie/BinanceChain/wire"
	"github.com/cosmos/cosmos-sdk/client/context"
	"github.com/cosmos/cosmos-sdk/client/keys"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	txbuilder "github.com/cosmos/cosmos-sdk/x/auth/client/txbuilder"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/spf13/viper"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount("bnc", "bncp")
	config.SetBech32PrefixForValidator("bva", "bvap")
	config.SetBech32PrefixForConsensusNode("bca",  "bcap")
	config.Seal()
}

const (
	retry = 25
	stime = 2000
	createTask = 1
	submitTask = 2
	buy = 1
	sell = 2
)

var home *string
var node *string
var chainId *string
var owner *string
var userPrefix *string
var batchSize *int
var generateToken *bool
var initiateAccount *bool
var runCreate *bool
var createChnBuf *int
var createPoolSize *int
var createPath *string
var runSubmit *bool
var submitChnBuf *int
var submitPoolSize *int
var submitPath *string
var submitPause *int64
var csvPath *string

type DEXCreate struct {
	ctx context.CLIContext
	txBldr txbuilder.TxBuilder
	addr sdk.AccAddress
	side int8
	symbol string
	price int64
	qty int64
	tifCode int8
}
type DEXSubmit struct {
	ctx context.CLIContext
	txBldr txbuilder.TxBuilder
	txBytes []byte
}

type sequence struct {
	m sync.Mutex
	seqMap map[string]int64
}
type txhash struct {
	m sync.Mutex
	trans []string
}

var createChn chan DEXCreate
var submitChn chan DEXSubmit

var clientSeq sequence
var hashReturned txhash

var nodes []string
var rpcs []*rpcclient.HTTP

func init() {
	home = flag.String("home", "/home/test/.bnbcli", "bnbcli --home")
	node = flag.String("node", "0.0.0.0:26657", "bnbcli --node")
	chainId = flag.String("chainId", "chain-bnb", "bnbcli --chain-id")
	owner = flag.String("owner", "test", "chain's master user")
	userPrefix = flag.String("userPrefix", "node2_user", "user prefix")
	batchSize = flag.Int("batchSize", 1, "# of create/submit tasks")
	generateToken = flag.Bool("generateToken", false, "if to generate tokens")
	initiateAccount = flag.Bool("initiateAccount", false, "if to initiate accounts")
	runCreate = flag.Bool("runCreate", false, "if to run create task")
	createChnBuf = flag.Int("createChnBuf", 1, "create channel buffer size")
	createPoolSize = flag.Int("createPoolSize", 1, "create pool size")
	createPath = flag.String("createPath", "/home/test/create", "create path")
	runSubmit = flag.Bool("runSubmit", false, "if to run submit task")
	submitChnBuf = flag.Int("submitChnBuf", 1, "submit channel buffer size")
	submitPoolSize = flag.Int("submitPoolSize", 1, "submit pool size")
	submitPath = flag.String("submitPath", "/home/test/submit", "submit path")
	submitPause = flag.Int64("submitPause", 0, "submit pause time in ms")
	csvPath = flag.String("csvPath", "/home/test", "csv path")
	flag.Parse()
	createChn = make(chan DEXCreate, *createChnBuf)
	submitChn = make(chan DEXSubmit, *submitChnBuf)
	clientSeq = sequence{seqMap: make(map[string]int64)}
	hashReturned = txhash{trans: make([]string,0,0)}
	nodes = strings.Split(*node, ",")
	rpcs = make([]*rpcclient.HTTP, len(nodes))
	for i, v := range nodes {
		rpcs[i] = rpcclient.NewHTTP(v, "/websocket")
	}
}

var accounts map[string]string
var sortKeys []string

func main() {
	fmt.Println("-home", *home)
	fmt.Println("-node", *node)
	fmt.Println("-chainId", *chainId)
	fmt.Println("-owner", *owner)
	fmt.Println("-userPrefix", *userPrefix)
	fmt.Println("-batchSize", *batchSize)
	fmt.Println("-generateToken", *generateToken)
	fmt.Println("-initiateAccount", *initiateAccount)
	fmt.Println("-runCreate", *runCreate)
	fmt.Println("-createChnBuf", *createChnBuf)
	fmt.Println("-createPoolSize", *createPoolSize)
	fmt.Println("-createPath", *createPath)
	fmt.Println("-runSubmit", *runSubmit)
	fmt.Println("-submitChnBuf", *submitChnBuf)
	fmt.Println("-submitPoolSize", *submitPoolSize)
	fmt.Println("-submitPath", *submitPath)
	fmt.Println("-submitPause", *submitPause)
	fmt.Println("-csvPath", *csvPath)

	lookupAccounts()

	tokens := generateTokens(0, 2, *generateToken)
	initializeAccounts(accounts, tokens, *initiateAccount)

	if *runCreate == true {
		createFolder(*createPath)
		emptyFolder(*createPath)
		sT := time.Now()
		doCreateTask(accounts, tokens)
		eT := time.Now()
		elapsed := eT.Sub(sT)
		fmt.Println("start:", sT)
		fmt.Println("end:", eT)
		fmt.Println("elapsed:", elapsed)
	}

	if *runSubmit == true {
		createFolder(*submitPath)
		emptyFolder(*submitPath)
		moveFiles(*createPath, *submitPath, *batchSize)
		sT := time.Now()
		doSubmitTask()
		eT := time.Now()
		elapsed := eT.Sub(sT)
		fmt.Println("start:", sT)
		fmt.Println("end:", eT)
		fmt.Println("elapsed:", elapsed)
	}

	// to generate data for AP and QS test
	save_txhash()
	save_hextx()
}

func execCommand(name string, arg ...string) *bytes.Buffer {
	var err error
	for i:= 0; i < retry; i++ {
		fmt.Println("running round", ":", i, name)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd := exec.Command(name, arg...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		if err != nil {
			fmt.Println(fmt.Sprint(err), stderr.String())
			continue
		}
		return &stdout
	}
	panic(err)
}

func lookupAccounts() {
	stdout := execCommand("bnbcli", "--home="+*home, "keys", "list")
	expr := "(" + *userPrefix + "[\\d]+).+(bnc.+).+bnc"
	res, err := regexp.Compile(expr)
	if err != nil {
		panic(err)
	}
	accounts = make(map[string]string)
	matched := res.FindAllStringSubmatch(stdout.String(), -1)
	if matched != nil {
		for _, v := range matched {
			accounts[v[1]] = v[2]
		}
	} else {
		panic("no accounts found")
	}
	sortKeys = make([]string, 0, len(accounts))
	for key, _ := range accounts {
		sortKeys = append(sortKeys, key)
	}
	sort.Strings(sortKeys)
}

func generateTokens(sI int, eI int, flag bool) []string {
	var tokens []string
	for sI <= eI {
		var token string
		if sI < 10 {
			token = fmt.Sprintf("X0%d", sI)
		} else if sI >= 10 && sI < 100 {
			token = fmt.Sprintf("X%d", sI)
		} else {
			panic("token index was out of range")
		}
		if flag == true {
			execCommand("bnbcli", "token", "issue", "--home="+*home, "--node="+*node, "--token-name="+token, "--symbol="+token, "--total-supply=20000000000000000", "--from="+*owner, "--chain-id="+*chainId)
			time.Sleep(stime * time.Millisecond)
			execCommand("bnbcli", "dex", "list", "--home="+*home, "--node="+*node, "--base-asset-symbol="+token, "--quote-asset-symbol=BNB", "--init-price=100000000", "--from="+*owner, "--chain-id="+*chainId)
			time.Sleep(stime * time.Millisecond)
		}
		tokens = append(tokens, token)
		sI++
	}
	return tokens
}

func initializeAccounts(accounts map[string]string, tokens []string, flag bool) {
	tokens = append(tokens, "BNB")
	if flag == true {
		for _, token := range tokens {
			var buffer bytes.Buffer
			for i, name := range sortKeys {
				buffer.WriteString(accounts[name])
				buffer.WriteString(":")
				if i != 0 && (i%2000 == 0 || i == len(sortKeys)-1) {
					fmt.Println(token, i)
					list := buffer.String()
					res := list[:len(list)-1]
					buffer.Reset()
					execCommand("bnbcli", "token", "multi-send", "--home="+*home, "--node="+*node, "--chain-id="+*chainId, "--from="+*owner, "--amount=10000000000:"+token, "--to="+res)
					time.Sleep(stime * time.Millisecond)
				}
			}
		}
	}
	tokens = tokens[:len(tokens)-1]
}

func createFolder(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		os.Mkdir(path, 0777)
	}
}

func emptyFolder(path string) {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		panic(err)
	}
	for _, file := range files {
		err = os.RemoveAll(filepath.Join(path, file.Name()))
		if err != nil {
			panic(err)
		}
	}
}

func execute(poolSize int, mode int) {
	var wg sync.WaitGroup
	wg.Add(poolSize)
	for i := 0; i < poolSize; i++ {
		if mode == createTask {
			go create(&wg, &clientSeq)
		}
		if mode == submitTask {
			go submit(&wg, &hashReturned)
		}
	}
	wg.Wait()
}

func doCreateTask(accounts map[string]string, tokens []string) {
	go allocateCreate(accounts, tokens)
	execute(*createPoolSize, createTask)
}

func allocateCreate(accounts map[string]string, tokens []string) {
	var buyPrices []int64 = generatePrices(*batchSize, 1.00)
	var sellPrices []int64 = generatePrices(*batchSize, 1.01)
	createIndex := 0
	nameIndex := 0
	for i := 0; i < *batchSize; i++ {
		for j := 0; j < len(tokens); j++ {
			symbol := fmt.Sprintf("%s_BNB", tokens[j])
			fmt.Printf("allocating #%d\n", createIndex)
			createChn <- buildC(sortKeys[nameIndex], buy, symbol, buyPrices[i], 100000000, "GTC")
			createIndex++
			if createIndex == *batchSize {
				close(createChn)
				return
			}
			nameIndex++
			if nameIndex == len(sortKeys) {
				nameIndex = 0
			}
			createChn <- buildC(sortKeys[nameIndex], sell, symbol, sellPrices[i], 100000000, "GTC")
			createIndex++
			if createIndex == *batchSize {
				close(createChn)
				return
			}
			nameIndex++
			if nameIndex == len(sortKeys) {
				nameIndex = 0
			}
		}
	}
}

func generatePrices(noOfPrices int, margin float64) []int64 {
	rand.Seed(1)
	prices := make([]int64, noOfPrices)
	for i := 0; i < noOfPrices; i++ {
		f := rand.Float64() + margin
		s := fmt.Sprintf("%.4f", f)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			panic(err)
		}
		prices[i] = int64(f*10000)*10000
	}
	return prices
}

func buildC(from string, side int8, symbol string, price int64, qty int64, tif string) DEXCreate {
	cdc := MakeCodec()
	viper.Set("home", fmt.Sprintf("%s", *home))
	viper.Set("node", nodes[0])
	viper.Set("chain-id", *chainId)
	viper.Set("from", from)
	viper.Set("trust-node", true)
	ctx, txBldr := client.PrepareCtx(cdc)
	ctx.Client = rpcs[0]
	addr, err := ctx.GetFromAddress()
	if err != nil {
		panic(err)
	}
	accNum, err := ctx.GetAccountNumber(addr)
	if err != nil {
		panic(err)
	}
	txBldr = txBldr.WithAccountNumber(accNum)
	tifCode, err := order.TifStringToTifCode(tif)
	if err != nil {
		panic(err)
	}
	return DEXCreate{ctx, txBldr, addr, side, symbol, price, qty, tifCode}
}

func MakeCodec() *wire.Codec {
	var cdc = wire.NewCodec()
	wire.RegisterCrypto(cdc)
	bank.RegisterCodec(cdc)
	sdk.RegisterCodec(cdc)
	dex.RegisterWire(cdc)
	tokens.RegisterWire(cdc)
	types.RegisterWire(cdc)
	tx.RegisterWire(cdc)
	return cdc
}

func create(wg *sync.WaitGroup, s *sequence) {
	for item := range createChn {
		name, err := item.ctx.GetFromName()
		if err != nil {
			fmt.Println(err)
			continue
		}
		s.m.Lock()
		seq, hasKey := s.seqMap[name]
		s.m.Unlock()
		if hasKey == false {
			var err error
			seq, err = item.ctx.GetAccountSequence(item.addr)
			if err != nil {
				fmt.Println(err)
				continue
			}
		}
		item.txBldr = item.txBldr.WithSequence(seq)
		id := fmt.Sprintf("%X-%d", item.addr, seq+1)
		msg := order.NewOrderMsg{
			Sender: item.addr,
			Id: id,
			Symbol: item.symbol,
			OrderType: order.OrderType.LIMIT,
			Side: item.side,
			Price: item.price,
			Quantity: item.qty,
			TimeInForce: order.TimeInForce.GTC,
		}
		msg.TimeInForce = item.tifCode
		msgs := []sdk.Msg{msg}
		ssMsg := txbuilder.StdSignMsg {
			ChainID: item.txBldr.ChainID,
			AccountNumber: item.txBldr.AccountNumber,
			Sequence: item.txBldr.Sequence,
			Memo: item.txBldr.Memo,
			Msgs: msgs,
		}
		keybase, err := keys.GetKeyBaseFromDir(*home)
		if err != nil {
			fmt.Println(err)
			continue
		}
		sigBytes, pubkey, err := keybase.Sign(name, "1qaz2wsx", ssMsg.Bytes())
		if err != nil {
			fmt.Println(err)
			continue
		}
		sig := auth.StdSignature {
			AccountNumber: ssMsg.AccountNumber,
			Sequence: ssMsg.Sequence,
			PubKey: pubkey,
			Signature: sigBytes,
		}
		txBytes, err := item.txBldr.Codec.MarshalBinary(auth.NewStdTx(ssMsg.Msgs, []auth.StdSignature{sig}, ssMsg.Memo))
		if err != nil {
			fmt.Println("failed to sign tran: %v", err)
			continue
		}
		ts := fmt.Sprintf("%d", time.Now().UnixNano())
		file := filepath.Join(*createPath, ts + "_" + name)
		fmt.Println("Acc-", item.txBldr.AccountNumber, "signed tran saved,", file)
		err = ioutil.WriteFile(file, txBytes, 0777)
		if err != nil {
			fmt.Println(err)
			continue
		}
		s.m.Lock()
		s.seqMap[name] = seq+1
		s.m.Unlock()
	}
	wg.Done()
}

func doSubmitTask() {
	go allocateSubmit()
	execute(*submitPoolSize, submitTask)
}

func allocateSubmit() {
	expr := "_(" + *userPrefix + "[\\d]+)$"
	res, err := regexp.Compile(expr)
	if err != nil {
		panic(err)
	}
	files, err := ioutil.ReadDir(*submitPath)
	if err != nil {
		panic(err)
	}
	nodeIndex := 0
	userNodeMap := make(map[string]int)
	for i, file := range files {
		matched := res.FindStringSubmatch(file.Name())
		if matched != nil {
			tran, err := ioutil.ReadFile(filepath.Join(*submitPath, file.Name()))
			if err != nil {
				panic(err)
			}
			fmt.Printf("allocate tran #%d\n", i)
			_, hasKey := userNodeMap[matched[1]]
			if hasKey == false {
				userNodeMap[matched[1]] = nodeIndex
			}
			submitChn <- buildS(userNodeMap[matched[1]], tran)
			nodeIndex++
			if nodeIndex == len(nodes) {
				nodeIndex = 0
			}
		} else {
			panic("invalid filename")
		}
	}
	close(submitChn)
}

func buildS(index int, txBytes []byte) DEXSubmit {
	cdc := MakeCodec()
	viper.Set("node", nodes[index])
	viper.Set("chain-id", *chainId)
	ctx, txBldr := client.PrepareCtx(cdc)
	ctx.Client = rpcs[index]
	return DEXSubmit{ctx, txBldr, txBytes}
}

func submit(wg *sync.WaitGroup, txh *txhash) {
	for item := range submitChn {
		async(item.ctx, item.txBldr, item.txBytes, txh)
		time.Sleep(time.Duration(*submitPause) * time.Millisecond)
	}
	wg.Done()
}

func async(ctx context.CLIContext, txBldr txbuilder.TxBuilder, txBytes []byte, txh *txhash) {
	defer doRecover()
	res, err := ctx.BroadcastTxAsync(txBytes)
	if err != nil {
		fmt.Println(err)
	}
	if ctx.JSON {
		type toJSON struct {
			TxHash string
		}
		valueToJSON := toJSON{res.Hash.String()}
		JSON, err := txBldr.Codec.MarshalJSON(valueToJSON)
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println(string(JSON))
	} else {
		str := res.Hash.String()
		txh.m.Lock()
		txh.trans = append(txh.trans, str)
		txh.m.Unlock()
		fmt.Println("tran hash:", str)
	}
}

func doRecover() {
	if r := recover(); r != nil {
		fmt.Println("recoved from", r)
		debug.PrintStack()
	}
}

func moveFiles(srcPath string, dstPath string, count int) {
	files, err := ioutil.ReadDir(srcPath)
	if err != nil {
		panic(err)
	}
	for i, file := range files {
		if i < count {
			src := filepath.Join(srcPath, file.Name())
			dst := filepath.Join(dstPath, file.Name())
			err := os.Rename(src, dst)
			if err != nil {
				panic(err)
			}
		}
	}
}

func save_txhash() {
	if len(hashReturned.trans) > 0 {
		path := filepath.Join(*csvPath, "txhash.csv")
		csvFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0777)
		if err != nil {
			panic(err)
		}
		defer csvFile.Close()
		writer := bufio.NewWriter(csvFile)
		for _, tran := range hashReturned.trans {
			_, err = writer.WriteString(tran + "\n")
			if err != nil {
				continue
			}
		}
		writer.Flush()
	}
}

func save_hextx() {
	path := filepath.Join(*csvPath, "trans.csv")
	csvFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0777)
	if err != nil {
		panic(err)
	}
	defer csvFile.Close()
	writer := bufio.NewWriter(csvFile)
	expr := "_(" + *userPrefix + "[\\d]+)$"
	res, err := regexp.Compile(expr)
	if err != nil {
		panic(err)
	}
	files, err := ioutil.ReadDir(*createPath)
	if err != nil {
		panic(err)
	}
	userIPMap := make(map[string]string)
	for _, file := range files {
		matched := res.FindStringSubmatch(file.Name())
		if matched != nil {
			_, hasKey := userIPMap[matched[1]]
			if hasKey == false {
				var buffer bytes.Buffer
				buffer.WriteString(strconv.Itoa(rand.Intn(256)))
				buffer.WriteString(".")
				buffer.WriteString(strconv.Itoa(rand.Intn(256)))
				buffer.WriteString(".")
				buffer.WriteString(strconv.Itoa(rand.Intn(256)))
				buffer.WriteString(".")
				buffer.WriteString(strconv.Itoa(rand.Intn(256)))
				ip := buffer.String()
				userIPMap[matched[1]] = ip
			}
			txBytes, err := ioutil.ReadFile(filepath.Join(*createPath, file.Name()))
			if err != nil {
				panic(err)
			}
			hexBytes := make([]byte, len(txBytes)*2)
			hex.Encode(hexBytes, txBytes)
			line := fmt.Sprintf("%s|%s|%s\n", accounts[matched[1]], userIPMap[matched[1]], hexBytes)
			_, err = writer.WriteString(line)
			if err != nil {
				continue
			}
		}
	}
	writer.Flush()
}