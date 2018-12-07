package main

// upload img
// choose deletion criteria
// option to delete using a password
// view img
// be simple
// be short
// be less enough

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
	minio "github.com/minio/minio-go"
	"github.com/mseshachalam/imgshare/pkg/resource"
	"github.com/satori/go.uuid"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/sync/errgroup"
)

const maxAllowedImgSize = 16 << 20

// Make a new bucket called images.
const bucketName = "resources"

const endpoint = "localhost:9000"
const accessKeyID = "AKIAIOSFODNN7EXAMPLE"
const secretAccessKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

const createResourcesTable = `CREATE TABLE IF NOT EXISTS resource (
	id	INTEGER PRIMARY KEY AUTOINCREMENT,
	uuid TEXT NOT NULL UNIQUE,
	created_on	datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
	deleted_on	datetime,
	uploaded_at	TEXT NOT NULL,
	caption	TEXT,
	content_type	TEXT NOT NULL,
	last_visit_on	datetime,
	destruct_key	BLOB,
	destruct_key_salt	BLOB
);`

const insertToResourcesTable = `INSERT INTO resource(uuid, uploaded_at, caption, content_type, destruct_key, destruct_key_salt) values(?,?,?,?,?,?)`

const location = "us-east-1"

var dir string
var secure bool
var authKey []byte
var minioClient *minio.Client
var db *sql.DB

func init() {
	var g errgroup.Group

	g.Go(func() (err error) {
		flag.StringVar(&dir, "dir", ".", "the directory to serve files from. Defaults to the current dir")
		flag.BoolVar(&secure, "secure", false, "weather the app is running on tls or not")

		// Initialize minio client object.
		minioClient, err = minio.New(endpoint, accessKeyID, secretAccessKey, secure)
		if err != nil {
			return
		}

		err = minioClient.MakeBucket(bucketName, location)
		if err != nil {
			// Check to see if we already own this bucket (which happens if you run this twice)
			exists, err := minioClient.BucketExists(bucketName)
			if err == nil && exists {
				log.Printf("We already own %s\n", bucketName)
			} else {
				return err
			}
		}

		log.Printf("Successfully created %s\n", bucketName)
		return nil
	})

	g.Go(func() (err error) {
		authKey, err = getRandomBytes(32)
		return
	})

	g.Go(func() (err error) {
		db, err = sql.Open("sqlite3", "./app.db")
		if err != nil {
			return
		}

		_, err = db.Exec(createResourcesTable)
		if err != nil {
			return
		}

		log.Printf("create table executed")

		return
	})

	err := g.Wait()
	if err != nil {
		panic(err)
	}
}

type app struct {
	DB          *sql.DB
	MinioClient *minio.Client
}

func getRandomBytes(n int) ([]byte, error) {
	randomBuf := make([]byte, 32)
	_, err := rand.Read(randomBuf)
	if err != nil {
		return nil, err
	}

	return randomBuf, nil
}

func (a *app) indexHandler(w http.ResponseWriter, r *http.Request) {
	// serve form to upload a single image with caption
	p := map[string]interface{}{
		"Title":          "Ummimg",
		"Headline":       "Upload Image",
		"Information":    fmt.Sprintf("Max allowed upload size is %dmb", maxAllowedImgSize/(1024*1024)),
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	t, _ := template.ParseFiles("templates/index.html")
	t.Execute(w, p)
}

func (a *app) uploadFileHandler(w http.ResponseWriter, r *http.Request) {
	var f resource.Resource

	u := uuid.Must(uuid.NewV4())
	f.UUID = u.String()

	err := r.ParseMultipartForm(maxAllowedImgSize)
	if err != nil {
		fmt.Fprintf(w, "Can not handle images bigger than %dmb, failed with %s", maxAllowedImgSize/(1024*1024), err)
		return
	}

	destructKey := r.FormValue("destruct-key")

	if len(destructKey) != 0 {
		destructKeySalt, err := getRandomBytes(32)
		if err != nil {
			fmt.Fprintf(w, "Internal server error %s", err)
			return
		}
		destructKey = strings.TrimSpace(destructKey)
		hash, _ := scrypt.Key([]byte(destructKey), destructKeySalt, 32768, 8, 1, 32)

		f.DestructKey = hash
		f.DestructKeySalt = destructKeySalt
	}

	caption := r.FormValue("caption")
	if len(caption) != 0 {
		f.Caption = strings.TrimSpace(caption)
	}

	file, handler, err := r.FormFile("resource")
	if err != nil {
		fmt.Fprintf(w, "Failed to retrieve img. Caused error is %s", err)
		return
	}
	defer file.Close()

	fileType := handler.Header.Get("Content-Type")
	if !strings.HasPrefix(fileType, "image/") {
		fmt.Fprintf(w, "could not determine the file type %s, please retry.", handler.Header.Get(""))
	}

	f.ContentType = fileType

	userMetadata := map[string]string{
		"name": handler.Filename,
	}

	_, err = a.MinioClient.PutObjectWithContext(r.Context(), bucketName, u.String(), file, handler.Size, minio.PutObjectOptions{UserMetadata: userMetadata, ContentType: fileType, ContentDisposition: handler.Header.Get("Content-Disposition")})

	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	reqParams := make(url.Values) // TODO: give all the goodies of image serving
	reqParams.Set("response-content-disposition", "attachment; filename=\\"+handler.Filename+"\"")

	// Generates a presigned url which expires in a day.
	presignedURL, err := a.MinioClient.PresignedGetObject(bucketName, u.String(), time.Second*24*60*60, reqParams)
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	f.UploadedAt = presignedURL.String()

	stmt, err := a.DB.Prepare(insertToResourcesTable)
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}
	defer stmt.Close()

	_, err = stmt.Exec(f.UUID, f.UploadedAt, f.Caption, f.ContentType, f.DestructKey, f.DestructKeySalt)
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/i/%s", f.UUID), http.StatusPermanentRedirect)
}

func (a *app) viewFileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	stmt, err := a.DB.Prepare("select created_on, uploaded_at, caption, content_type from resource where uuid = ?")
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	var f resource.Resource

	uuid := vars["id"]
	row := stmt.QueryRow(uuid)
	err = row.Scan(&f.CreatedOn, &f.UploadedAt, &f.Caption, &f.ContentType)
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	p := map[string]interface{}{
		"Title":          "Ummimg",
		"Caption":        f.Caption,
		"Src":            f.UploadedAt,
		"Uuid":           uuid,
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	t, _ := template.ParseFiles("templates/view.html")
	t.Execute(w, p)
}

func (a *app) deleteFileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	stmt, err := a.DB.Prepare("select destruct_key, destruct_key_salt from resource where uuid = ?")
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	var f resource.Resource

	uuid := vars["id"]

	row := stmt.QueryRow(uuid)
	err = row.Scan(&f.DestructKey, &f.DestructKeySalt)
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	destructKey := strings.TrimSpace(r.FormValue("destruct-key"))

	if len(destructKey) != 0 {
		dk, err := scrypt.Key([]byte(destructKey), f.DestructKeySalt, 32768, 8, 1, 32)
		if err != nil {
			fmt.Fprintf(w, "error is %s", err)
			return
		}
		if bytes.Compare(dk, f.DestructKey) == 0 {
			_, err = a.DB.Exec("DELETE FROM resource WHERE uuid = ?;", uuid)
			// delete the file
			err = a.MinioClient.RemoveObject(bucketName, uuid)
			if err != nil {
				fmt.Println(err)
				http.Redirect(w, r, "/", http.StatusPermanentRedirect)
			}
		} else {
			log.Println("wrong creds")
		}
	} else {
		log.Println("stay on the same page with error")
	}
}

func main() {
	defer db.Close()
	a := &app{
		MinioClient: minioClient,
		DB:          db,
	}

	r := mux.NewRouter()
	r.HandleFunc("/", a.indexHandler).Methods("GET")
	r.HandleFunc("/", a.uploadFileHandler).Methods("POST")
	r.HandleFunc("/i/{id}", a.viewFileHandler).Methods("GET")
	r.HandleFunc("/i/{id}", a.deleteFileHandler).Methods("POST")

	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir(dir))))

	http.Handle("/", csrf.Protect(authKey, csrf.Secure(false))(r))

	log.Fatal(http.ListenAndServe(":8080", nil))
}
