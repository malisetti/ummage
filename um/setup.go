package um

import (
	"database/sql"
	"log"

	minio "github.com/minio/minio-go"
)

func SetupStorage(secure bool, dir, endpoint, accessKeyID, secretAccessKey, bucketName, location string) (*minio.Client, error) {
	minioClient, err := minio.New(endpoint, accessKeyID, secretAccessKey, secure)
	if err != nil {
		return nil, err
	}

	err = minioClient.MakeBucket(bucketName, location)
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, err := minioClient.BucketExists(bucketName)
		if err == nil && exists {
			log.Printf("We already own %s\n", bucketName)
		} else {
			return nil, err
		}
	}

	log.Printf("Successfully created %s\n", bucketName)
	return minioClient, nil
}

func SetupDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS resource (
			id	INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid TEXT NOT NULL UNIQUE,
			name TEXT,
			caption	TEXT,
			content_type	TEXT NOT NULL,
			last_visit_on	datetime,
			destruct_key	BLOB,
			destruct_key_salt	BLOB
		);`)

	if err != nil {
		return nil, err
	}

	log.Println("create table executed")
	return db, nil
}
