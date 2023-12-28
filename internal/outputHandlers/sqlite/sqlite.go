package sqlite

import (
	"database/sql"
	"encoding/json"
	"log"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

type SqliteOutput struct {
	Database string
	db       *sql.DB
	reqChan  chan string
	resChan  chan string
	wg       sync.WaitGroup

	dbLock sync.Mutex
}

func (o *SqliteOutput) Init() {
	if o.Database == "" {
		log.Panic("sqlite database file not set")
	}

	db, err := sql.Open("sqlite3", o.Database)
	if err != nil {
		log.Panic(err)
	}
	o.db = db

	createReq := "CREATE TABLE IF NOT EXISTS requests (id integer not null primary key, request text);"

	_, err = db.Exec(createReq)
	if err != nil {
		log.Panicf("failed to create table %q: %s\n", err, createReq)
		return
	}

	o.dbLock = sync.Mutex{}
	//Buffered channel as the requests/responses can come in bursts
	o.reqChan = make(chan string, 20)
	o.wg = sync.WaitGroup{}
	o.wg.Add(1)
	go func() {
		insertReq := "INSERT into requests(request) values(?);"
		for r := range o.reqChan {
			o.dbLock.Lock()
			_, err = db.Exec(insertReq, r)
			o.dbLock.Unlock()
			if err != nil {
				log.Printf("failed to insert request %q: %s\n", err, insertReq)
				return
			}
		}
		o.wg.Done()
	}()

	o.resChan = make(chan string, 20)
	createRes := "CREATE TABLE IF NOT EXISTS responses (id integer not null primary key, response text);"
	_, err = db.Exec(createRes)
	if err != nil {
		log.Panicf("failed to create table %q: %s\n", err, createRes)
		return
	}
	o.wg.Add(1)
	go func() {
		insertRes := "INSERT into responses(response) values(?);"
		for r := range o.resChan {
			o.dbLock.Lock()
			_, err = db.Exec(insertRes, r)
			o.dbLock.Unlock()
			if err != nil {
				log.Printf("failed to insert response %q: %s\n", err, insertRes)
				return
			}
		}
		o.wg.Done()
	}()
}

func (o *SqliteOutput) Cleanup() {
	close(o.reqChan)
	o.wg.Wait()
	o.db.Close()
}

type request struct {
	TransactionIdentifier string              `json:"transactionId"` //The coresponding response and request will have the same uuid
	Origin                string              `json:"origin"`
	Method                string              `json:"method"`
	Body                  string              `json:"body"`
	Url                   string              `json:"url"`
	Path                  string              `json:"path"`
	Raw                   string              `json:"raw"`
	Host                  string              `json:"host"`
	Headers               map[string][]string `json:"headers"`
}

type response struct {
	TransactionIdentifier string              `json:"transactionId"` //The coresponding response and request will have the same uuid
	Body                  string              `json:"body"`
	Headers               map[string][]string `json:"headers"`
	StatusCode            int                 `json:"status_code"`
	StatusLine            string              `json:"status_line"`
}

// The go sqlite driver does not allow for concurrent writes, so there must only be one "SqliteOutput" object used, but HandleRequest is safe to use by multipe go routines
func (o *SqliteOutput) HandleRequest(transactionIdentifier, origin, method, body, url, path, raw, host string, headers map[string][]string) error {
	r := request{TransactionIdentifier: transactionIdentifier, Origin: origin, Method: method, Body: body, Url: url, Path: path, Raw: raw, Host: host, Headers: headers}
	rjson, err := json.Marshal(r)
	if err != nil {
		return err
	}

	o.reqChan <- string(rjson)

	return nil
}

func (o *SqliteOutput) HandleResponse(transactionIdentifier, body, statusLine string, statusCode int, headers map[string][]string) error {
	r := response{TransactionIdentifier: transactionIdentifier, Body: body, StatusCode: statusCode, StatusLine: statusLine, Headers: headers}
	rjson, err := json.Marshal(r)
	if err != nil {
		return err
	}

	o.resChan <- string(rjson)

	return nil
}
