package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/supergiant/supergiant/pkg/account"
	"github.com/supergiant/supergiant/pkg/api"
	"github.com/supergiant/supergiant/pkg/helm"
	"github.com/supergiant/supergiant/pkg/jwt"
	"github.com/supergiant/supergiant/pkg/kube"
	"github.com/supergiant/supergiant/pkg/profile"
	"github.com/supergiant/supergiant/pkg/runner/ssh"
	"github.com/supergiant/supergiant/pkg/storage"
	"github.com/supergiant/supergiant/pkg/templatemanager"
	"github.com/supergiant/supergiant/pkg/testutils/assert"
	"github.com/supergiant/supergiant/pkg/user"
	"github.com/supergiant/supergiant/pkg/util"
	"github.com/supergiant/supergiant/pkg/workflows"
)

type Server struct {
	server http.Server
	cfg    *Config
}

func (srv *Server) Start() {
	err := srv.server.ListenAndServe()
	if err != nil {
		logrus.Fatal(err)
	}
}

func (srv *Server) Shutdown() {
	err := srv.server.Close()
	if err != nil {
		logrus.Fatal(err)
	}
}

// Config is the server configuration
type Config struct {
	Port         int
	Addr         string
	EtcdUrl      string
	LogLevel     string
	TemplatesDir string
}

func New(cfg *Config) (*Server, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}

	configureLogging(cfg)
	r, err := configureApplication(cfg)
	if err != nil {
		return nil, err
	}
	headersOk := handlers.AllowedHeaders([]string{"Access-Control-Request-Headers", "Authorization"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS"})

	// TODO add TLS support
	s := &Server{
		cfg: cfg,
		server: http.Server{
			Handler:      handlers.CORS(headersOk, methodsOk)(handlers.RecoveryHandler(handlers.PrintRecoveryStack(true))(r)),
			Addr:         fmt.Sprintf("%s:%d", cfg.Addr, cfg.Port),
			ReadTimeout:  time.Second * 10,
			WriteTimeout: time.Second * 15,
			IdleTimeout:  time.Second * 120,
		},
	}
	if err := generateUserIfColdStart(cfg); err != nil {
		return nil, err
	}

	return s, nil
}

//generateUserIfColdStart checks if there are any users in the db and if not (i.e. on first launch) generates a root user
func generateUserIfColdStart(cfg *Config) error {
	etcdCfg := clientv3.Config{
		Endpoints:   []string{cfg.EtcdUrl},
		DialTimeout: 10 * time.Second,
	}

	repository := storage.NewETCDRepository(etcdCfg)
	userService := user.NewService(user.DefaultStoragePrefix, repository)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	users, err := userService.GetAll(ctx)
	if err != nil {
		return err
	}

	if len(users) == 0 {
		u := &user.User{
			Login:    "root",
			Password: util.RandomString(13),
		}
		logrus.Infof("first time launch detected, use %s as login and %s as password", u.Login, u.Password)
		err := userService.Create(ctx, u)
		if err != nil {
			return nil
		}
	}

	return nil
}

func validate(cfg *Config) error {
	if cfg.EtcdUrl == "" {
		return errors.New("etcd url can't be empty")
	}

	if err := assert.CheckETCD(cfg.EtcdUrl); err != nil {
		return errors.Wrapf(err, "etcd url %s", cfg.EtcdUrl)
	}

	if cfg.Port <= 0 {
		return errors.New("port can't be negative")
	}

	return nil
}
func configureApplication(cfg *Config) (*mux.Router, error) {
	//TODO will work for now, but we should revisit ETCD configuration later
	etcdCfg := clientv3.Config{
		Endpoints: []string{cfg.EtcdUrl},
	}
	router := mux.NewRouter()

	protectedAPI := router.PathPrefix("/v1/api").Subrouter()
	repository := storage.NewETCDRepository(etcdCfg)

	accountService := account.NewService(account.DefaultStoragePrefix, repository)
	accountHandler := account.NewHandler(accountService)
	accountHandler.Register(protectedAPI)

	kubeService := kube.NewService(kube.DefaultStoragePrefix, repository)
	kubeHandler := kube.NewHandler(kubeService)
	kubeHandler.Register(protectedAPI)

	//TODO Add generation of jwt token
	jwtService := jwt.NewTokenService(86400, []byte("test"))
	userService := user.NewService(user.DefaultStoragePrefix, repository)
	userHandler := user.NewHandler(userService, jwtService)

	router.HandleFunc("/auth", userHandler.Authenticate).Methods(http.MethodPost)
	//Opening it up for testing right now, will be protected after implementing initial user generation
	protectedAPI.HandleFunc("/users", userHandler.Create).Methods(http.MethodPost)

	kubeProfileService := profile.NewKubeProfileService(profile.DefaultKubeProfilePreifx, repository)
	kubeProfileHandler := profile.NewKubeProfileHandler(kubeProfileService)
	kubeProfileHandler.Register(protectedAPI)

	nodeProfileService := profile.NewNodeProfileService(profile.DefaultNodeProfilePrefix, repository)
	nodeProfileHandler := profile.NewNodeProfileHandler(nodeProfileService)
	nodeProfileHandler.Register(protectedAPI)

	// Read templates first and then initialize workflows with steps that uses these templates
	if err := templatemanager.Init(cfg.TemplatesDir); err != nil {
		return nil, err
	}
	workflows.Init()

	taskHandler := workflows.NewTaskHandler(repository, ssh.NewRunner, accountService)
	taskHandler.Register(router)

	helmService := helm.NewService(repository)
	helmHandler := helm.NewHandler(helmService)
	helmHandler.Register(protectedAPI)

	authMiddleware := api.Middleware{
		TokenService: jwtService,
		UserService:  userService,
	}
	protectedAPI.Use(authMiddleware.AuthMiddleware, api.ContentTypeJSON)

	return router, nil
}

func configureLogging(cfg *Config) {
	l, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		logrus.Warnf("incorrect logging level %s provided, setting INFO as default...", l)
		logrus.SetLevel(logrus.InfoLevel)
		return
	}
	logrus.SetLevel(l)
}
