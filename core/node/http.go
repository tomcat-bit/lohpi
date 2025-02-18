package node

import (
	"bytes"
	"strings"
	"encoding/json"
	"errors"
	pb "github.com/arcsecc/lohpi/protobuf"
	"fmt"
	"context"
//	"github.com/arcsecc/lohpi/core/comm"
	"github.com/arcsecc/lohpi/core/util"
	"github.com/gorilla/mux"
_	"google.golang.org/protobuf/types/known/timestamppb"
	"github.com/rs/cors"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jws"
	"github.com/lestrrat-go/jwx/jwa"
	log "github.com/sirupsen/logrus"
	"net/http"
	pbtime "google.golang.org/protobuf/types/known/timestamppb"
	"strconv"
	"time"
)

func (n *NodeCore) startHTTPServer() error {
	router := mux.NewRouter()
	addr := fmt.Sprintf(":%d", n.config().Port)
	
	log.WithFields(log.Fields{
		"entity": "Lohpi directory server",
		"port": n.config().Port,
	}).Infoln("Started HTTP server")

	dRouter := router.PathPrefix("/dataset").Schemes("HTTP").Subrouter()
	dRouter.HandleFunc("/ids", n.getDatasetIdentifiers).Methods("GET")
	dRouter.HandleFunc("/info/{id:.*}", n.getDatasetSummary).Methods("GET")
	dRouter.HandleFunc("/new_policy/{id:.*}", n.setDatasetPolicy).Methods("PUT")
	dRouter.HandleFunc("/data/{id:.*}", n.getDataset).Methods("GET")
	dRouter.HandleFunc("/metadata/{id:.*}", n.getMetadata).Methods("GET")
	dRouter.HandleFunc("/addset/{id:.*}", n.addDataset).Methods("POST")
	dRouter.HandleFunc("/removeset/{id:.*}", n.removeDataset).Methods("GET")

	dRouter.HandleFunc("/test/{id:.*}", n.test).Methods("GET")

	// Middlewares used for validation
	dRouter.Use(n.middlewareValidateTokenSignature)
	//dRouter.Use(n.middlewareValidateTokenClaims)

	handler := cors.AllowAll().Handler(router)

	n.httpServer = &http.Server{
		Addr:         addr,
		Handler:      handler,
		WriteTimeout: time.Hour * 30,
		ReadTimeout:  time.Hour * 30,
		IdleTimeout:  time.Hour * 60,
		//TLSConfig:    comm.ServerConfig(n.cu.Certificate(), n.cu.CaCertificate(), n.cu.PrivateKey()),
	}

	if err := n.setPublicKeyCache(); err != nil {
		log.Errorln(err.Error())
		return err
	}

	return n.httpServer.ListenAndServe()
}

// TODO redirect HTTP to HTTPS
func redirectTLS(w http.ResponseWriter, r *http.Request) {
    //http.Redirect(w, r, "https://IPAddr:443"+r.RequestURI, http.StatusMovedPermanently)
}

func (n *NodeCore) setPublicKeyCache() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n.pubKeyCache = jwk.NewAutoRefresh(ctx)
	const msCerts = "https://login.microsoftonline.com/common/discovery/v2.0/keys" // TODO: config me

	n.pubKeyCache.Configure(msCerts, jwk.WithRefreshInterval(time.Minute * 5))

	// Keep the cache warm
	_, err := n.pubKeyCache.Refresh(ctx, msCerts)
	if err != nil {
		log.Println("Failed to refresh Microsoft Azure JWKS")
		return err
	}
	return nil
}

// defer r.Body.Close()?

func (n *NodeCore) middlewareValidateTokenSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := getBearerToken(r)
		if err != nil {
			panic(err)
			http.Error(w, http.StatusText(http.StatusBadRequest)+": "+err.Error(), http.StatusBadRequest)
			return
		}
		_ = token
		/*
		if err := n.validateTokenSignature(token); err != nil {
			panic(err)
			http.Error(w, http.StatusText(http.StatusBadRequest)+": "+err.Error(), http.StatusBadRequest)
			return
		}*/

		next.ServeHTTP(w, r)
	})
}

func (n *NodeCore) validateTokenSignature(token []byte) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// TODO: fetch keys again if it fails
	// TODO: add clock skew. Use jwt.WithAcceptableSkew
	set, err := n.pubKeyCache.Fetch(ctx, "https://login.microsoftonline.com/common/discovery/v2.0/keys")
	if err != nil {
		return err
	}

	// Note: see https://github.com/lestrrat-go/jwx/blob/a7f076fc6eadb44380d41b5e30eb5a85a91de864/jws/jws.go#L186
	// There is no guarantee that the algorithm is RS256
	for it := set.Iterate(context.Background()); it.Next(context.Background()); {
		_, err := jws.Verify(token, jwa.RS256, it.Pair().Value)
		if err == nil {
			return nil
		}
	}

	// Fetch the public keys again from URL if the verification failed.
	// Fetching it again will guarantee a best-effort to verify the request.
	/*ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// This doesn't work!
	// TODO: this doesn't work
	set, err := d.ar.Refresh(ctx, msCerts)
	if err != nil {
		log.Println("Failed to refresh Microsoft Azure JWKS")
		return err
	}

	// Note: see https://github.com/lestrrat-go/jwx/blob/a7f076fc6eadb44380d41b5e30eb5a85a91de864/jws/jws.go#L186
	// There is no guarantee that algorithm is RS256
	for it := set.Iterate(context.Background()); it.Next(context.Background()); {
		_, err := jws.Verify(token, jwa.RS256, it.Pair().Value)
		if err == nil {
			return nil
		}
	}*/

	return errors.New("Could not verify token")
}

func (n *NodeCore) test(w http.ResponseWriter, r *http.Request) {
	datasetId := mux.Vars(r)["id"]
	d := n.dsManager.Dataset(datasetId)
	fmt.Fprintf(w, "DS: %+v\n", d)
}

func (n *NodeCore) removeDataset(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	datasetId := strings.Split(r.URL.Path, "/dataset/removeset/")[1]
	if datasetId == "" {
		err := fmt.Errorf("Dataset identifier must not be empty.")
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}	

	if !n.dsManager.DatasetExists(datasetId) {
		err := fmt.Errorf("Dataset '%s' is not indexed by the server", datasetId)
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotFound) + ": " + err.Error(), http.StatusNotFound)
		return
	}

	if err := n.dsManager.RemoveDataset(datasetId); err != nil {
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError) + ": " + err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Sucsessfully removed dataset '%s' to the node\n", datasetId);
}

func (n *NodeCore) addDataset(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	datasetId := strings.Split(r.URL.Path, "/dataset/addset/")[1]
	if datasetId == "" {
		err := fmt.Errorf("Dataset identifier must not be empty.")
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}	

	newDataset := &pb.Dataset{
		Identifier: datasetId,
		Policy: &pb.Policy{},
	}

	if err := n.dsManager.InsertDataset(datasetId, newDataset); err != nil {
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Sucsessfully added dataset '%s' to the node\n", datasetId);
}

func (n *NodeCore) getMetadata(w http.ResponseWriter, r *http.Request) {
	datasetId := strings.Split(r.URL.Path, "/dataset/metadata/")[1]
	
	if !n.dsManager.DatasetExists(datasetId) {
		err := fmt.Errorf("Dataset '%s' is not indexed by the server", datasetId)
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotFound) + ": " + err.Error(), http.StatusNotFound)
		return
	}

	// TODO: strip the headers from information about the client
	if handler := n.getMetadataHandler(); handler != nil {
		handler(datasetId, w, r)
	} else {
		err := fmt.Errorf("Metadata handler is nil")
		log.Warnln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotImplemented) + ": " + err.Error(), http.StatusNotImplemented)
		return
	}
}

func getBearerToken(r *http.Request) ([]byte, error) {
	authHeader := r.Header.Get("Authorization")
	authHeaderContent := strings.Split(authHeader, " ")
	if len(authHeaderContent) != 2 {
		return nil, errors.New("Token not provided or malformed")
	}
	return []byte(authHeaderContent[1]), nil
}

// TODO use context!
func (n *NodeCore) getDataset(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	token, err := getBearerToken(r)
	if err != nil {
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}

	pbClient, err := jwtTokenToPbClient(string(token))
	if err != nil {
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}

	datasetId := strings.Split(r.URL.Path, "/dataset/data/")[1]
	if !n.dsManager.DatasetExists(datasetId) {
		err := fmt.Errorf("Dataset '%s' is not indexed by the server", datasetId)
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotFound) + ": " + err.Error(), http.StatusNotFound)
		return
	}

	// TODO: use client attributes to determine access. nil here??
	policy := n.dsManager.GetDatasetPolicy(datasetId)
	log.Println("POLICY: ", policy)
	if !policy.GetContent() {
		err := fmt.Errorf("You do not have access to this dataset")
		http.Error(w, http.StatusText(http.StatusUnauthorized) + ": " + err.Error(), http.StatusUnauthorized)
		return
	}

	// TODO: strip the headers from information about the client
	if handler := n.getDatasetHandler(); handler != nil {
		handler(datasetId, w, r)
		// TODO: write to responseWriter only once. What happens if anything goes wrong at any point in time?
	} else {
		err := fmt.Errorf("Dataset download is not implemented for this service")
		log.Warnln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotImplemented) + ": " + err.Error(), http.StatusNotImplemented)
		return
	}

	checkout := &pb.DatasetCheckout{
		DatasetIdentifier: datasetId,
		DateCheckout: pbtime.Now(),
		Client: pbClient,
	}
	if err := n.dcManager.CheckoutDataset(datasetId, checkout); err != nil {
		log.Error(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError) + ": " + err.Error(), http.StatusInternalServerError)
		return
	}
}

// Returns the dataset identifiers stored at this node
func (n *NodeCore) getDatasetIdentifiers(w http.ResponseWriter, r *http.Request) {
	log.Infoln("Got request to", r.URL.String())
	defer r.Body.Close()

	ids := n.dsManager.DatasetIdentifiers()
	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(ids); err != nil {
		log.Errorln(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Add("Content-Type", "application/json")
	w.Header().Add("Content-Length", strconv.Itoa(len(ids)))

	_, err := w.Write(b.Bytes())
	if err != nil {
		log.Errorln(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError) + ": " + err.Error(), http.StatusInternalServerError)
		return
	}
}

func (n *NodeCore) DatasetIsCheckedOut(datasetId string, client *pb.Client) (bool, error) {
	//return n.dcManager.DatasetIsCheckedOut(datasetId, pbClient)
	return false, nil
}

// Returns a JSON object containing the metadata assoicated with a dataset
func (n *NodeCore) getDatasetSummary(w http.ResponseWriter, r *http.Request) {
	/*log.Infoln("Got request to", r.URL.String())
	defer r.Body.Close()

	dataset := mux.Vars(r)["id"]
	if dataset == "" {
		err := errors.New("Missing dataset identifier")
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}

	if !n.dsManager.DatasetExists(dataset) {
		err := fmt.Errorf("Dataset '%s' is not indexed by the server", dataset)
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotFound) + ": " + err.Error(), http.StatusNotFound)
		return
	}

	// Fetch the policy
	policy := n.dsManager.DatasetPolicy(dataset)
	if policy == nil {
		log.Errorln("Policy is nil")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Fetch the clients that have checked out the dataset
	_, err := n.dcManager.DatasetCheckouts(dataset)
	if err != nil {
		log.Errorln(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Destination struct
	resp := struct {
		Dataset   string
		Policy    string
		//Checkouts []CheckoutInfo
	}{
		Dataset:   dataset,
		//Policy:    policy,
		//Checkouts: checkouts,
	}

	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(resp); err != nil {
		log.Errorln(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	r.Header.Add("Content-Length", strconv.Itoa(len(b.Bytes())))

	_, err = w.Write(b.Bytes())
	if err != nil {
		log.Errorln(err.Error())
		http.Error(w, http.StatusText(http.StatusInternalServerError) + ": " + err.Error(), http.StatusInternalServerError)
		return
	}*/
}

/* Assigns a new policy to the dataset. The request body must be a JSON object with a string similar to this:
 *{
 *	"policy": true/false
 *}
 */
// TODO: restrict this function only to be used to set the initial dataset policy.
// SHOULD NOT BE USED
func (n *NodeCore) setDatasetPolicy(w http.ResponseWriter, r *http.Request) {
	log.Infoln("Got request to", r.URL.String())
	defer r.Body.Close()

	// Enforce application/json MIME type
	if r.Header.Get("Content-Type") != "application/json" {
		msg := "Content-Type header is not application/json"
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType)+": "+msg, http.StatusUnsupportedMediaType)
		return
	}

	// Fetch the dataset if
	dataset := mux.Vars(r)["id"]
	if dataset == "" {
		err := errors.New("Missing dataset identifier")
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusBadRequest) + ": " + err.Error(), http.StatusBadRequest)
		return
	}

	// Check if dataset exists in the external repository
	if !n.dsManager.DatasetExists(dataset) {
		err := fmt.Errorf("Dataset '%s' is not stored in this node", dataset)
		log.Infoln(err.Error())
		http.Error(w, http.StatusText(http.StatusNotFound) + ": " + err.Error(), http.StatusNotFound)
		return
	}

	// Destination struct
	reqBody := struct {
		Policy string
	}{}

	if err := util.DecodeJSONBody(w, r, "application/json", &reqBody); err != nil {
		var e *util.MalformedParserReponse
		if errors.As(err, &e) {
			log.Errorln(err.Error())
			http.Error(w, e.Msg, e.Status)
		} else {
			log.Errorln(err.Error())
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
		return
	}


/*	if err := n.dsManager.SetDatasetPolicy(dataset, reqBody.Policy); err != nil {
		log.Warnln(err.Error())
	}*/

	respMsg := "Successfully set a new policy for " + dataset + "\n"
	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/text")
	w.Header().Set("Content-Length", strconv.Itoa(len(respMsg)))
	w.Write([]byte(respMsg))
}
