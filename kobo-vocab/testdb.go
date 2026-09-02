package main

import (
	"database/sql"
	"log"
	"os"
)

// --- Test DB ---

func createTestDB(path string) {
	os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	db.Exec(`CREATE TABLE WordList (Text TEXT, VolumeId TEXT, DictSuffix TEXT, DateCreated TEXT)`)
	for _, w := range []struct{ t, v, d, c string }{
		{"behoefte", "1939166523", "-nl-en", "2025-12-03T07:32:30Z"},
		{"besmettelijk", "1973941266", "-nl-en", "2026-02-07T14:06:17Z"},
		{"felle", "1934193436", "-nl-en", "2025-11-22T16:59:33Z"},
		{"kwaad", "1934193436", "-nl-en", "2025-11-22T17:00:17Z"},
		{"leek", "1934639860", "-nl-en", "2025-11-23T12:05:15Z"},
		{"letten", "1936121537", "-nl-en", "2025-11-26T19:56:27Z"},
		{"onafhankelijk", "1937667690", "-nl-en", "2025-11-28T23:59:59Z"},
		{"ongemakkelijk", "1940692249", "-nl-en", "2025-12-08T22:03:32Z"},
		{"raakt", "1936121537", "-nl-en", "2025-11-26T19:47:49Z"},
		{"ramp", "1967258575", "-nl-en", "2026-01-31T18:42:36Z"},
		{"redt", "1936121537", "-nl-en", "2025-11-26T19:45:43Z"},
		{"vaardigheid", "1936121537", "-nl-en", "2025-11-26T19:49:42Z"},
		{"verschuift", "1936121537", "-nl-en", "2025-11-26T19:49:14Z"},
		{"voorwerpen", "1933028413", "-nl-en", "2025-11-20T21:54:29Z"},
		{"zoals", "1973941266", "-nl-en", "2026-02-07T14:07:14Z"},
	} {
		db.Exec(`INSERT INTO WordList VALUES(?,?,?,?)`, w.t, w.v, w.d, w.c)
	}
	log.Printf("Created test DB %s", path)
}
