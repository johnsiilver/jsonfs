# jsonfs - absolutely NOT production ready.

This was a thought experiment on a tool for doing JSON discovery where you
don't have a schema.

It treats the JSON as a filesystem. Directories represents JSON dictionaries and arrays.  Files represents JSON basic values.

These tie in with an in memory `fs.FS` filesystem that will allow you to use filesystem tools to work with the data.  This seems nicer to use than a `map[string]any` or `[]any` that you might otherwise deal with from the `stdlib`.

There is a diskfs that also implements `fs.FS` . I have not put it through its paces. The idea was to allow you to copy whatever you want from the `memfs` to the `diskfs` and then you could use all the filesystem tools that Linux gives you.  

At this point, what I guarantee is that the Marshal() and Unmarshal() do work. The Unmarshal() is slower because we provide more structures, which means it takes longer.

Marshal() on the other hand is faster and much less memory intensive. 

The statemachines for marshal and unmarshal are pretty good. At some point I may decouple those into their own library to allow building anyone to build JSON tooling.

Here are the benchmarks I referred to:

```
BenchmarkMarshalJSONSmall-10          	  256087	      4573 ns/op	       0 B/op	       0 allocs/op
BenchmarkMarshalJSONStdlibSmall-10    	  241816	      4981 ns/op	    2304 B/op	      51 allocs/op
BenchmarkMarshalJSONLarge-10          	       7	 143744732 ns/op	 9633560 B/op	   11255 allocs/op
BenchmarkMarshalJSONStdlibLarge-10    	       7	 143856131 ns/op	101807982 B/op	 1571144 allocs/op
BenchmarkUnmarshalSmall-10            	  134758	      8933 ns/op	    7281 B/op	     173 allocs/op
BenchmarkStdlibUnmarshalSmall-10    	  209151	      5903 ns/op	    2672 B/op	      66 allocs/op
BenchmarkUnmarshalLarge-10            	       4	 250373635 ns/op	240508578 B/op	 5336230 allocs/op
BenchmarkStdlibUnmarshalLarge-10    	       6	 187222674 ns/op	99413457 B/op	 1750078 allocs/op
BenchmarkObjectWalk-10                	       4	 284417094 ns/op	145693142 B/op	 3442589 allocs/op
BenchmarkStdlibObjectWalk-10          	      10	 104694233 ns/op	58166933 B/op	 1551677 allocs/op
```