# Why We Switched from Microservices to a Monolith

Status: draft
Author: Engineering Team
Target: engineering blog, developer audience

## Introduction

After three years running 47 microservices, we consolidated back to a
monolith. This post explains why, what we learned, and who should
consider the same move.

## The problem

Our microservices architecture served us well during rapid growth from
5 to 50 engineers. But as the team stabilized at 30, the operational
overhead became the bottleneck, not the codebase.

Key pain points:
- 23 different deployment pipelines to maintain
- Cross-service debugging took 3x longer than monolith-era debugging
- Schema changes required coordinated deploys across 5+ services

## The migration

We migrated incrementally over 6 months, service by service, using the
strangler fig pattern. Each service was absorbed into the monolith
behind its existing API contracts.

## Results

- Deployment time: 45 min -> 8 min
- Mean time to debug: 4 hours -> 1.5 hours
- Infrastructure cost: reduced 40%

## Conclusion

Microservices solve organizational scaling problems, not technical ones.
If your team has stabilized, reconsider whether the complexity is still
paying for itself.
