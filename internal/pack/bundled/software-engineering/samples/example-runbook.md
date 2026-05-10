# Runbook: High Memory Usage Alert

## Trigger

Memory usage exceeds 85% on any application pod for more than 5 minutes.

## Impact

Potential OOM kills, degraded response times, cascading failures.

## Diagnostic steps

1. Check current memory usage: `kubectl top pods -n production`
2. Identify the specific pod: look for pods using >85% of their memory limit
3. Check recent deployments: `kubectl rollout history deployment/app -n production`
4. Review application logs: `kubectl logs <pod-name> -n production --tail=100`

## Resolution

1. If caused by a memory leak: restart the affected pod
2. If caused by increased load: scale horizontally
3. If caused by a recent deployment: rollback to the previous version

## Prevention

- Set memory limits on all containers
- Enable heap profiling in staging
- Run load tests before production deployments
