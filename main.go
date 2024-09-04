package main

// BUCKET_NAME=bucket YC_ACCESS_KEY_ID=yc_access_key_id YC_SECRET_ACCESS_KEY=yc_secret_access_key
// SERVICE_ACCOUNT_ID=service_account KEY_ID=key_id SECRET_KEY_PATH=privatekey.pem YC_BILLING_ID=yc_billing_id go run main.go

import (
	"context"
	"crypto/rsa"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/golang-jwt/jwt/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	BaseUrl string = "https://billing.api.cloud.yandex.net/billing/v1/billingAccounts/"
)

type Metrics struct {
	Balance                 prometheus.Gauge
	CurrentDayUsageAmount   prometheus.Gauge
	CurrentMonthUsageAmount prometheus.Gauge
}

type ycBillingResponse struct {
	CreatedAt   time.Time `json:"createdAt"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CountryCode string    `json:"countryCode"`
	Currency    string    `json:"currency"`
	Balance     string    `json:"balance"`
	Active      bool      `json:"active"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	logger.Info("Yandex cloud billing exporter is running...")

	serviceAccountID, ok := os.LookupEnv("SERVICE_ACCOUNT_ID")
	if !ok {
		slog.Error("SERVICE_ACCOUNT_ID not set")
		os.Exit(1)
	}

	keyID, ok := os.LookupEnv("KEY_ID")
	if !ok {
		slog.Error("KEY_ID not set")
		os.Exit(1)
	}

	secretKeyPath, ok := os.LookupEnv("SECRET_KEY_PATH")
	if !ok {
		slog.Error("SECRET_KEY_PATH not set")
		os.Exit(1)
	}

	ycBillingId, ok := os.LookupEnv("YC_BILLING_ID")
	if !ok {
		slog.Error("YC_BILLING_ID not set")
		os.Exit(1)
	}

	bucketName, ok := os.LookupEnv("BUCKET_NAME")
	if !ok {
		slog.Error("BUCKET_NAME not set")
		os.Exit(1)
	}

	go recordMetrics(serviceAccountID, keyID, secretKeyPath, ycBillingId, bucketName)

	srv := &http.Server{
		Addr:    ":2112",
		Handler: promhttp.Handler(),
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen: ", "err", err)
		}
	}()
	slog.Info("Server Started")

	<-done

	slog.Info("Http server stopped")
}

func s3ClientAuth() *s3.Client {
	customEndpoint := "https://storage.yandexcloud.net"
	region := "ru-central1"

	accessKeyID, ok := os.LookupEnv("YC_ACCESS_KEY_ID")
	if !ok {
		slog.Error("YC_ACCESS_KEY_ID not set")
		os.Exit(1)
	}

	secretAccessKey, ok := os.LookupEnv("YC_SECRET_ACCESS_KEY")
	if !ok {
		slog.Error("YC_SECRET_ACCESS_KEY not set")
		os.Exit(1)
	}

	credProvider := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credProvider),
	)

	if err != nil {
		slog.Error("Couldn't load default configuration ", "err", err)
	}
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(customEndpoint)
	})
	return s3Client
}

func getLastReport(bucketName string) {
	client := s3ClientAuth()

	slog.Info("Getting list of objects at S3 bucket")
	result, err := client.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})

	if err != nil {
		slog.Error("Couldn't get list objects from bucket", "err", err)
	}

	if len(result.Contents) == 0 {
		slog.Error("no objects found in the bucket")
	}

	slog.Info("Sort list of objects at S3 bucket for getting LastModified")
	sort.Slice(result.Contents, func(i, j int) bool {
		return result.Contents[i].LastModified.After(*result.Contents[j].LastModified)
	})

	// Get last report
	lastReport := result.Contents[0]

	ctx := context.Background()

	// Get the object of last report from S3
	getReport, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(*lastReport.Key),
	})

	if err != nil {
		slog.Error("Unable to get object from S3", "err", err)
	}

	defer getReport.Body.Close()

	slog.Info("Create file report.csv")
	file, err := os.Create("report.csv")
	if err != nil {
		slog.Error("Unable to create file", "err", err, "file", file)
	}
	defer file.Close()

	slog.Info("Write to file report.csv info from S3 object")
	_, err = io.Copy(file, getReport.Body)
	if err != nil {
		slog.Error("Unable to write object content to file", "err", err)
	}

}

func getCurrentMonthAmountUsage(bucketName string) float64 {
	client := s3ClientAuth()

	slog.Info("Getting list of objects at S3 bucket")
	result, err := client.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})

	t := time.Now()
	month := t.Month()

	ctx := context.Background()

	var monthSum float64
	for obj := range result.Contents {
		if month == result.Contents[obj].LastModified.Month() {

			slog.Info("Create file report.csv")
			file, err := os.Create("report.csv")
			if err != nil {
				slog.Error("Unable to create file", "err", err, "file", file)
			}
			defer file.Close()

			getDailyReport, err := client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucketName),
				Key:    aws.String(*result.Contents[obj].Key),
			})

			if err != nil {
				slog.Error("Unable to get object from S3", "err", err)
			}

			defer getDailyReport.Body.Close()

			slog.Info("Write to file report.csv info from S3 object")
			_, err = io.Copy(file, getDailyReport.Body)
			if err != nil {
				slog.Error("Unable to write object content to file", "err", err)
			}

			slog.Info("Open file report.csv for parsing Cost column")
			f, err := os.Open("report.csv")
			if err != nil {
				slog.Error("Unable to read input file", "err", err)
			}
			defer f.Close()

			slog.Info("Parse file report.csv")
			csvReader := csv.NewReader(f)
			records, err := csvReader.ReadAll()
			if err != nil {
				slog.Error("Unable to parse file as CSV for ", "err", err)
			}

			columnIndex := 14

			var sum float64
			for _, record := range records {
				if columnIndex > 0 {
					if cellValue, err := strconv.ParseFloat(record[columnIndex], 64); err == nil {
						sum += cellValue

					}
				}
			}
			monthSum += sum
		}

	}

	if err != nil {
		slog.Error("Couldn't get list objects from bucket", "err", err)
	}

	if len(result.Contents) == 0 {
		slog.Error("no objects found in the bucket")
	}
	return monthSum
}

func parseCsvReport(filePath string, bucketName string) float64 {
	getLastReport(bucketName)
	slog.Info("Open file report.csv for parsing Cost column")
	f, err := os.Open(filePath)
	if err != nil {
		slog.Error("Unable to read input file", "filePath", filePath, "err", err)
	}
	defer f.Close()

	slog.Info("Parse file report.csv")
	csvReader := csv.NewReader(f)
	records, err := csvReader.ReadAll()
	if err != nil {
		slog.Error("Unable to parse file as CSV for ", "filePath", filePath, "err", err)
	}

	columnIndex := 14

	var column []string
	var sum float64
	for _, record := range records {
		column = append(column, record[columnIndex])
		if columnIndex > 0 {
			if cellValue, err := strconv.ParseFloat(record[columnIndex], 64); err == nil {
				sum += cellValue
			}
		}
	}

	slog.Info("Extracted column Cost: ", "Cost", column)

	slog.Info("Day amount of usage: ", "sum", sum)

	return sum
}

func recordMetrics(serviceAccountID string, keyID string, secretKeyPath string, ycBillingId string, bucketName string) {
	gauge := initMetrics()
	slog.Info("Record prometeus metric")
	for {
		getToken := exchangeJWTToIAM(serviceAccountID, keyID, secretKeyPath)
		bl, _ := getYandexCloudBilling(getToken, ycBillingId)
		gauge.Balance.Set(bl)
		gauge.CurrentDayUsageAmount.Set(parseCsvReport("report.csv", bucketName))
		gauge.CurrentMonthUsageAmount.Set(getCurrentMonthAmountUsage(bucketName))
		err := os.Remove("report.csv")
		if err != nil {
			slog.Error("Can't remove file", "err", err)
		}
		time.Sleep(time.Hour * 1)
	}
}

func initMetrics() *Metrics {
	slog.Info("Build prometeus metric")

	return &Metrics{
		Balance: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "yc_billing_balance",
			Help: "The total balance of Yandex cloud account",
		}),
		CurrentDayUsageAmount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "yc_billing_current_day_usage_amount",
			Help: "Current day usage amount of Yandex cloud account",
		}),
		CurrentMonthUsageAmount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "yc_billing_current_month_usage_amount",
			Help: "Current month usage amount of Yandex cloud account",
		}),
	}
}

func createJWTToken(serviceAccountID string, keyID string, keyFile string) string {
	claims := jwt.RegisteredClaims{
		Issuer:    serviceAccountID,
		ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(1 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
		NotBefore: jwt.NewNumericDate(time.Now().UTC()),
		Audience:  []string{"https://iam.api.cloud.yandex.net/iam/v1/tokens"},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodPS256, claims)
	token.Header["kid"] = keyID

	privateKey := loadPrivateKey(keyFile)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		slog.Error("Error get JWT token: ", "err", err)
	}

	return signed
}

func loadPrivateKey(keyFile string) *rsa.PrivateKey {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		slog.Error("Can't read privatekey file: ", "err", err)
	}
	rsaPrivateKey, err := jwt.ParseRSAPrivateKeyFromPEM(data)
	if err != nil {
		slog.Error("Can't parse privatekey file: ", "err", err)
	}
	return rsaPrivateKey
}

func exchangeJWTToIAM(serviceAccountID string, keyID string, keyFile string) string {
	jot := createJWTToken(serviceAccountID, keyID, keyFile)
	resp, err := http.Post(
		"https://iam.api.cloud.yandex.net/iam/v1/tokens",
		"application/json",
		strings.NewReader(fmt.Sprintf(`{"jwt":"%s"}`, jot)),
	)
	if err != nil {
		slog.Error("Can't make request to IAM API: ", "err", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		panic(fmt.Sprintf("%s: %s", resp.Status, body))
	}
	var data struct {
		IAMToken string `json:"iamToken"`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		slog.Error("Can't decode json from IAM API request: ", "err", err)
	}

	return data.IAMToken
}

func getYandexCloudBilling(iamToken string, ycBillingId string) (float64, error) {
	client := &http.Client{}
	ycMetrics := ycBillingResponse{}

	URL := BaseUrl + ycBillingId

	slog.Info("Trying get info about balance of Yandex cloud")
	req, err := http.NewRequest(http.MethodGet, URL, nil)

	slog.Info("Try authorize at Yandex cloud")
	if err != nil {
		slog.Error("Can't make auth request")
		return 0, err
	}
	req.Header.Add("Authorization", "Bearer "+iamToken)

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Can't get response")
		return 0, err
	}
	defer resp.Body.Close()
	temp, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Can't read response body")
		return 0, err
	}

	if err := json.Unmarshal(temp, &ycMetrics); err != nil {
		slog.Error("Can't make unmarshal json")
		return 0, err
	}

	flBalance, err := strconv.ParseFloat(ycMetrics.Balance, 64)
	if err != nil {
		slog.Error("Can't convert string to float64", "error", err, "balance_string", ycMetrics.Balance)
		// Return 0 as balance and the error
		return 0, err
	}
	slog.Info("Received value of balance of Yandex cloud", "balance", flBalance)

	return flBalance, nil
}
