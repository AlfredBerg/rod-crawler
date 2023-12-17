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
	wg       sync.WaitGroup
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
	o.reqChan = make(chan string, 20)
	o.wg = sync.WaitGroup{}
	o.wg.Add(1)
	go func() {
		insertReq := "INSERT into requests(request) values(?);"
		for r := range o.reqChan {
			_, err = db.Exec(insertReq, r)
			if err != nil {
				log.Printf("failed to insert request %q: %s\n", err, insertReq)
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
	Origin  string              `json:"origin"`
	Method  string              `json:"method"`
	Body    string              `json:"body"`
	Url     string              `json:"url"`
	Path    string              `json:"path"`
	Raw     string              `json:"raw"`
	Host    string              `json:"host"`
	Headers map[string][]string `json:"headers"`
}

// The go sqlite driver does not allow for concurrent writes, so there must only be one "SqliteOutput" object used, but HandleRequest is safe to use by multipe go routines
func (o *SqliteOutput) HandleRequest(origin, method, body, url, path, raw, host string, headers map[string][]string) error {
	r := request{Origin: origin, Method: method, Body: body, Url: url, Path: path, Raw: raw, Host: host, Headers: headers}
	rjson, err := json.Marshal(r)
	if err != nil {
		return err
	}
	log.Println(string(rjson))

	o.reqChan <- string(rjson)

	return nil
}
