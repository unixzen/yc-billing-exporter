# yc-billing-exporter
Prometheus exporter for getting information about billing account of Yandex Cloud.

## Before you begin

First of all, need [create service account](https://yandex.cloud/en-ru/docs/iam/operations/sa/create) at Yandex cloud.
After creating service account you need give permissions `billing.account.viewer` at level of [Organization](https://org.yandex.cloud/acl) at Yandex cloud. Next you need issue [Authorized keys](https://yandex.cloud/en-ru/docs/iam/concepts/authorization/key) they will be use for getting IAM-token at this step you need save `privateKey` and `id`.

> [!CAUTION]
> Private key of authorized keys is secret information which make execute operations at Yandex cloud. Private key need store at secure place.

## How it works

Yc billing exporter once at hour get IAM token and make request to [Billing API](https://yandex.cloud/ru/docs/billing/api-ref/BillingAccount/get) for getting remaining of money at balance.

## Configuration

For running yc-billing-exporter you need set next environment variables:
1. `YC_BILLING_ID` - Yandex cloud billing ID
2. `SERVICE_ACCOUNT_ID` - Service account ID
3. `KEY_ID` - Open key ID which you get from step issue Authorized keys
4. `SECRET_KEY_PATH` - Path at local file system where store `privateKey` (Later may be will be add some manager of secrets)

## How run

Build docker image

```sh
docker build -t yc-billing-exporter .
```

Create `docker-compose.yml` with next content

```yaml
version: '3'
services:
  yc_billing_exporter:
    image: yc-billing-exporter:latest
    ports:
      - "2112:2112"
    restart: always
    environment:
      SERVICE_ACCOUNT_ID: "<YOUR-SERVICE-ACCOUNT-ID>"
      KEY_ID: "<YOUR-KEY-ID>"
      SECRET_KEY_PATH: "<YOUR-SECRET-KEY-PATH>"
      YC_BILLING_ID: "<YOUR-YC-BILLING-ID>"
```

Run

```sh
docker compose up -d
```

Check correct start

```sh
docker compose logs -f
```

URL with metrics

`http://YOUR-IP:2112/metrics`

## Configuration of prometheus/victoriametrics (static configuration)

```yaml
 - job_name: 'yc_billing_exporter'
    scrape_interval: 60m
    static_configs:
      - targets: ['yc_billing_exporter_ip:6789']
```

## Example of alert for alertmanager

```yaml

- alert: yandex_cloud_billing
  expr: yc_billing_balance{job="yc_billing_exporter"}  < 50000
  for: 20m
  labels:
    severity: warning
  annotations:
    summary: "{{ $labels.instance }}: At Yandex cloud less than 50 000 rubles"
    description: "Your balance at danger zone"
```