This repo is a collection of vm statistics, 
gathered from a geth instance. 

Examples from a `m5.2xlargge` aws instance that did a full-sync. 

### Time spent

![What the evm spends time on](charts/timespent.png)

The large thing there is `SLOAD`. 


## Cost of ops

Are operations well-balanced, gas-wise. 
Let's see

![1](charts/timepergas0.png)

![2](charts/timepergas5.png)

![3](charts/timepergas10.png)

![4](charts/timepergas15.png)

![5](charts/timepergas20.png)
