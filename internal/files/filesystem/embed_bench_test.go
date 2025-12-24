package filesystem

import (
	"testing"
)

// BenchmarkEmbedFileSystem_Open benchmarks directory opening operations
func BenchmarkEmbedFileSystem_Open(b *testing.B) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dir, err := efs.Open(".")
		if err != nil {
			b.Fatal(err)
		}
		_ = dir
	}
}

// BenchmarkEmbedFileSystem_ReadFile benchmarks file reading operations
func BenchmarkEmbedFileSystem_ReadFile(b *testing.B) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		content, err := efs.ReadFile("root.sql")
		if err != nil {
			b.Fatal(err)
		}
		_ = content
	}
}

// BenchmarkEmbedFileSystem_Stat benchmarks stat operations
func BenchmarkEmbedFileSystem_Stat(b *testing.B) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		info, err := efs.Stat("root.sql")
		if err != nil {
			b.Fatal(err)
		}
		_ = info
	}
}

// BenchmarkEmbedFileSystem_Walk benchmarks directory walking
func BenchmarkEmbedFileSystem_Walk(b *testing.B) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")
	dir, err := efs.Open(".")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := dir.Walk(func(file File, err error) error {
			if err != nil {
				return err
			}
			_ = file
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryFileSystem_Open benchmarks in-memory directory opening
func BenchmarkMemoryFileSystem_Open(b *testing.B) {
	mfs := NewMemoryFileSystem("/test")
	mfs.AddFile("file1.sql", "SELECT 1;")
	mfs.AddFile("file2.sql", "SELECT 2;")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dir, err := mfs.Open("/test")
		if err != nil {
			b.Fatal(err)
		}
		_ = dir
	}
}

// BenchmarkMemoryFileSystem_ReadFile benchmarks in-memory file reading
func BenchmarkMemoryFileSystem_ReadFile(b *testing.B) {
	mfs := NewMemoryFileSystem("/test")
	mfs.AddFile("test.sql", "SELECT 1;")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		content, err := mfs.ReadFile("test.sql")
		if err != nil {
			b.Fatal(err)
		}
		_ = content
	}
}

// BenchmarkMemoryFileSystem_Walk benchmarks in-memory directory walking
func BenchmarkMemoryFileSystem_Walk(b *testing.B) {
	mfs := NewMemoryFileSystem("/test")
	mfs.AddFile("file1.sql", "SELECT 1;")
	mfs.AddFile("subdir/file2.sql", "SELECT 2;")

	dir, err := mfs.Open("/test")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := dir.Walk(func(file File, err error) error {
			if err != nil {
				return err
			}
			_ = file
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFileSystemComparison compares different filesystem implementation operations
func BenchmarkFileSystemComparison(b *testing.B) {
	b.Run("EmbedFS-ReadFile", func(b *testing.B) {
		efs := NewEmbedFileSystem(testdataFS, "testdata")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := efs.ReadFile("root.sql")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("MemoryFS-ReadFile", func(b *testing.B) {
		mfs := NewMemoryFileSystem("/test")
		mfs.AddFile("root.sql", "SELECT 1;\n")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := mfs.ReadFile("root.sql")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("EmbedFS-Walk", func(b *testing.B) {
		efs := NewEmbedFileSystem(testdataFS, "testdata")
		dir, _ := efs.Open(".")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			dir.Walk(func(file File, err error) error {
				return nil
			})
		}
	})

	b.Run("MemoryFS-Walk", func(b *testing.B) {
		mfs := NewMemoryFileSystem("/test")
		mfs.AddFile("root.sql", "SELECT 1;\n")
		mfs.AddFile("subdir/nested.sql", "SELECT 2;\n")
		dir, _ := mfs.Open("/test")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			dir.Walk(func(file File, err error) error {
				return nil
			})
		}
	})
}
