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
	"github.com/mseshachalam/ummage/pkg/resource"
	"github.com/satori/go.uuid"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/sync/errgroup"
)

const presignedURLExpiry = time.Second * 1

const maxAllowedImgSize = 1 << 20 // bytes

// Make a new bucket called images.
const bucketName = "resources"

const endpoint = "localhost:9000"
const accessKeyID = "AKIAIOSFODNN7EXAMPLE"
const secretAccessKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

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
	DB                    *sql.DB
	ResourceStorageClient *minio.Client
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
		"Title":          "Ummage",
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

	r.Body = http.MaxBytesReader(w, r.Body, maxAllowedImgSize)
	err := r.ParseMultipartForm(maxAllowedImgSize)
	if err != nil {
		fmt.Fprintf(w, "Can not handle images bigger than %dmb, failed with %s", maxAllowedImgSize/(1024*1024), err)
		return
	}

	file, handler, err := r.FormFile("resource")
	if err != nil {
		fmt.Fprintf(w, "Can not handle images bigger than %dmb, failed with %s", maxAllowedImgSize/(1024*1024), err)
		return
	}
	defer file.Close()

	destructKey := r.FormValue("destruct-key")

	if len(destructKey) != 0 {
		destructKeySalt, err := getRandomBytes(32)
		if err != nil {
			fmt.Fprintf(w, "Internal server error %s", err)
			return
		}
		hash, _ := scrypt.Key([]byte(destructKey), destructKeySalt, 32768, 8, 1, 32)

		f.DestructKey = hash
		f.DestructKeySalt = destructKeySalt
	}

	caption := r.FormValue("caption")
	if len(caption) != 0 {
		f.Caption = strings.TrimSpace(caption)
	}

	fileType := handler.Header.Get("Content-Type")
	if !strings.HasPrefix(fileType, "image/") {
		fmt.Fprintf(w, "Could not determine the file type %s, please retry.", handler.Header.Get(""))
	}

	f.ContentType = fileType

	userMetadata := map[string]string{
		"name": handler.Filename,
	}

	f.Name = handler.Filename

	_, err = a.ResourceStorageClient.PutObjectWithContext(r.Context(), bucketName, u.String(), file, handler.Size, minio.PutObjectOptions{UserMetadata: userMetadata, ContentType: fileType, CacheControl: "private, max-age=-1, no-cache, no-store, must-revalidate", ContentDisposition: handler.Header.Get("Content-Disposition")})

	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	stmt, err := a.DB.Prepare(`INSERT INTO resource(uuid, name, caption, content_type, destruct_key, destruct_key_salt) values(?,?,?,?,?,?)`)
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}
	defer stmt.Close()

	_, err = stmt.Exec(f.UUID, f.Name, f.Caption, f.ContentType, f.DestructKey, f.DestructKeySalt)
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	// show upload response page
	p := map[string]interface{}{
		"Title":   "Ummage",
		"Caption": f.Caption,
		"Uuid":    f.UUID,
	}

	t, _ := template.ParseFiles("templates/upload-response.html")
	t.Execute(w, p)
}

func (a *app) viewFileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	stmt, err := a.DB.Prepare("select name, caption, content_type from resource where uuid = ?")
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	var f resource.Resource

	uuid := vars["id"]
	row := stmt.QueryRow(uuid)
	err = row.Scan(&f.Name, &f.Caption, &f.ContentType)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Requested image not found %s", err)
		return
	}

	reqParams := make(url.Values) // TODO: give all the goodies of image serving
	reqParams.Set("response-content-disposition", "attachment; filename=\\"+f.Name+"\"")
	reqParams.Set("Cache-Control", "private, max-age=-1, no-cache, no-store, must-revalidate")

	// Generates a presigned url which expires in a day.
	uploadedAt, err := a.ResourceStorageClient.PresignedGetObject(bucketName, uuid, presignedURLExpiry, reqParams)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	p := map[string]interface{}{
		"Title":          "Ummage",
		"Caption":        f.Caption,
		"Src":            uploadedAt,
		"Uuid":           uuid,
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	t, _ := template.ParseFiles("templates/view.html")
	t.Execute(w, p)
}

func (a *app) deleteFileViewHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	uuid := vars["id"]

	stmt, err := a.DB.Prepare("select caption from resource where uuid = ?")
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	var caption string
	row := stmt.QueryRow(uuid)
	err = row.Scan(&caption)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Requested image not found %s", err)
		return
	}

	p := map[string]interface{}{
		"Title":          "Ummage",
		"Caption":        caption,
		"Uuid":           uuid,
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	t, _ := template.ParseFiles("templates/destruct.html")
	t.Execute(w, p)
}

func (a *app) deleteFileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	destructKey := r.FormValue("destruct-key")
	if len(destructKey) == 0 {
		fmt.Fprintf(w, "Please retry with a non empty destruct key")
		return
	}

	stmt, err := a.DB.Prepare("select destruct_key, destruct_key_salt from resource where uuid = ?")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	var f resource.Resource

	uuid := vars["id"]

	row := stmt.QueryRow(uuid)
	err = row.Scan(&f.DestructKey, &f.DestructKeySalt)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Requested image not found %s", err)
		return
	}

	dk, err := scrypt.Key([]byte(destructKey), f.DestructKeySalt, 32768, 8, 1, 32)
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}
	if bytes.Compare(dk, f.DestructKey) == 0 {
		_, err = a.DB.Exec("DELETE FROM resource WHERE uuid = ?;", uuid)
		// delete the file
		err = a.ResourceStorageClient.RemoveObject(bucketName, uuid)
		if err == nil {
			fmt.Fprintf(w, "Image is deleted")
			return
		}
	} else {
		fmt.Fprintf(w, "Possible wrong credentials, please try again.")
		return
	}
}

func main() {
	defer db.Close()
	a := &app{
		ResourceStorageClient: minioClient,
		DB:                    db,
	}

	CSRF := csrf.Protect(authKey, csrf.FieldName("Ummage-csrf"), csrf.CookieName("Ummage-cookie"), csrf.Secure(secure))

	r := mux.NewRouter()
	r.HandleFunc("/", a.indexHandler).Methods("GET")
	r.HandleFunc("/", a.uploadFileHandler).Methods("POST")

	r.HandleFunc("/i/{id}", a.viewFileHandler).Methods("GET")

	r.HandleFunc("/i/{id}/destruct", a.deleteFileViewHandler).Methods("GET")
	r.HandleFunc("/i/{id}/destruct", a.deleteFileHandler).Methods("POST")

	http.Handle("/", CSRF(r))

	log.Fatal(http.ListenAndServe(":8080", nil))
}
