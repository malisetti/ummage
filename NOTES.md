To generate certs for https server

https://marcofranssen.nl/build-a-go-webserver-on-http-2-using-letsencrypt

mkdir -p certs
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
    -keyout certs/localhost.key -out certs/localhost.crt \
    -subj "/C=IN/ST=Andhra Pradesh/L=Krishna/O=Seshachalam Malisetti/OU=Development/CN=localhost/emailAddress=abbiya@gmail.com"