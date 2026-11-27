package agentproxy

// WaitLearning blocks until every learn goroutine p started has finished.
func WaitLearning(p *Proxy) { p.learning.Wait() }
