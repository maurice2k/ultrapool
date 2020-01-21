package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	cryptoRand "crypto/rand"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	wp_gammazero "github.com/gammazero/workerpool"
	"github.com/maurice2k/ultrapool"
	wp_fasthttp "github.com/maurice2k/ultrapool/benchmark/fasthttp"
	wp_ants "github.com/panjf2000/ants/v2"

	"testing"
)

var wg sync.WaitGroup

var aesKey = []byte("0123456789ABCDEF")
var oneKiloByte = []byte(strings.Repeat("a", 1024))
var eightKiloByte = []byte(strings.Repeat("a", 8192))

// Encrypts given cipher text (prepended with the IV) with AES-128 or AES-256
// (depending on the length of the key)
func encryptCBC(plainText, key []byte) (cipherText []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	plainText = pad(aes.BlockSize, plainText)

	cipherText = make([]byte, aes.BlockSize+len(plainText))
	iv := cipherText[:aes.BlockSize]
	_, err = io.ReadFull(cryptoRand.Reader, iv)
	if err != nil {
		return nil, err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText[aes.BlockSize:], plainText)

	return cipherText, nil
}

// Adds PKCS#7 padding (variable block length <= 255 bytes)
func pad(blockSize int, buf []byte) []byte {
	padLen := blockSize - (len(buf) % blockSize)
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(buf, padding...)
}


func taskHandler(task ultrapool.Task) {
	time.Sleep(time.Microsecond)
	//sha256.Sum256(oneKiloByte)
	//encryptCBC(oneKiloByte, aesKey)
	//encryptCBC(eightKiloByte, aesKey)

	wg.Done()
}

func taskHandlerConn(conn net.Conn) error {
	time.Sleep(time.Microsecond)
	//sha256.Sum256(oneKiloByte)
	//encryptCBC(oneKiloByte, aesKey)
	//encryptCBC(eightKiloByte, aesKey)

	wg.Done()
	return nil
}

func BenchmarkNaiveGoRoutine(b *testing.B) {
	runtime.GC()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			wg.Add(1)
			c := new(net.TCPConn)
			go taskHandler(c)
		}
	})

	wg.Wait()
}

func BenchmarkUltrapoolWorkerpool(b *testing.B) {
	wp := ultrapool.NewWorkerPool(taskHandler)
	wp.SetIdleWorkerLifetime(time.Second)
	wp.SetNumShards(runtime.GOMAXPROCS(0))
	wp.Start()

	runtime.GC()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			wg.Add(1)
			c := new(net.TCPConn)
			wp.AddTask(c)
		}
	})

	wg.Wait()

	b.StopTimer()
	wp.Stop()
}

func BenchmarkAntsWorkerpool(b *testing.B) {
	wp, _ := wp_ants.NewPoolWithFunc(10000000, func(task interface{}) {
		taskHandler(task.(ultrapool.Task))
	}, wp_ants.WithPreAlloc(false))

	runtime.GC()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			wg.Add(1)
			c := new(net.TCPConn)
			wp.Invoke(c)
		}
	})

	wg.Wait()

	b.StopTimer()
	wp.Release()
}

func BenchmarkFasthttpWorkerpool(b *testing.B) {
	wp := &wp_fasthttp.WorkerPool{
		WorkerFunc:            taskHandlerConn,
		MaxWorkersCount:       10000000,
		LogAllErrors:          false,
		Logger:                nil,
		MaxIdleWorkerDuration: time.Second * 15,
	}
	wp.Start()

	runtime.GC()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			wg.Add(1)
			c := new(net.TCPConn)
			wp.Serve(c)
		}
	})

	wg.Wait()

	b.StopTimer()
	wp.Stop()
}

func BenchmarkGammazeroWorkerpool(b *testing.B) {
	wp := wp_gammazero.New(10000000)

	runtime.GC()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			wg.Add(1)
			c := new(net.TCPConn)
			wp.Submit(func() {
				taskHandlerConn(c)
			})
		}
	})

	wg.Wait()

	b.StopTimer()
	wp.Stop()
}

