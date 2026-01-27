# TODO

## Bound goroutine count

Currently, there are a few uses of goroutines that can lead to having an unbounded number of goroutines.

1. Callbacks run in dedicated one-off goroutines
2. Automatic resubscriptions run in dedicated one-off goroutines
3. etc

We need to find better alternatives.

## Make subscription tracking optional 

The *SubscribeSync methods could receive an option to request auto-resubscription explicitly, if not, then the subscription doesn't need to be tracked centrally at submux.

Alternatively, re-subscription could be determined by the callback function when processing a message type notifying of a Redis connection issue that implies the subscription doesn't exist anymore.
