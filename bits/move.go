package bits

import (
	"fmt"
	"io"
)

//Move will attempt to move all keys read from key reader 'kr' to the
//(remote)store configured in Config 'conf' and outputs pushed keys
//to key writer 'kw'.
func Move(kr KeyReader, kw KeyWriter, conf Config) error {

	//result of working the item
	type result struct {
		err error
	}

	//work item
	type item struct {
		key   K
		resCh chan *result
		err   error
	}

	//setup stores based on configuration
	src, err := conf.Stores.MoveSrc()
	if err != nil {
		return fmt.Errorf("couldnt get store to move from: %v", err)
	}

	dst, err := conf.Stores.MoveDst()
	if err != nil {
		return fmt.Errorf("couldnt get store to move to: %v", err)
	}

	//concurrent work
	work := func(it *item) {
		chunk, err := src.Get(it.key)
		if err != nil {
			it.resCh <- &result{fmt.Errorf("failed to get chunk '%s' from store: %v", it.key, err)}
			return
		}

		err = dst.Put(it.key, chunk)
		if err != nil {
			it.resCh <- &result{fmt.Errorf("failed to put chunk '%s' to remote: %v", it.key, err)}
			return
		}

		it.resCh <- &result{}
	}

	//if the dst is an remote store and an index is configured
	//fill the index first so we can prevent unnessary work
	var idx KeyIndex
	if remote, ok := dst.(RemoteStore); ok {
		if conf.Index != nil {
			idx = conf.Index
			err := remote.Index(idx)
			if err != nil {
				return fmt.Errorf("failed to index remote: %v", err)
			}
		}
		//@TODO always create memory index if the store is indexable
	}

	//fan-out
	itemCh := make(chan *item, conf.MoveConcurrency)
	go func() {
		defer close(itemCh)
		for {
			k, err := kr.Read()
			if err != nil {
				if err != io.EOF {
					itemCh <- &item{
						key: k,
						err: fmt.Errorf("failed to iterate into next key: %v", err),
					}
				}

				break
			}

			//we may be able to skip work altogether if an index
			//is present and it contains the key we intent to work on
			if idx != nil && idx.Has(k) {
				continue
			}

			it := &item{
				key:   k,
				resCh: make(chan *result),
			}

			go work(it)  //create work
			itemCh <- it //send to fan-in thread for syncing results
		}
	}()

	//fan-in
	for it := range itemCh {
		if it.err != nil {
			return fmt.Errorf("failed to iterate: %v", it.err)
		}

		res := <-it.resCh
		if res.err != nil {
			return res.err
		}

		err := kw.Write(it.key)
		if err != nil {
			return fmt.Errorf("handler failed for key '%s': %v", it.key, err)
		}

	}

	return nil
}
