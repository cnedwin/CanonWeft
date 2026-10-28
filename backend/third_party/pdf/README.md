# PDF 导出第三方许可

PDF由Go本地生成，版本由go.mod/go.sum锁定：

- github.com/signintech/gopdf v0.38.3：MIT，gopdf-LICENSE.txt。
- github.com/phpdave11/gofpdi v1.0.14-0.20211212211723-1f10f9844311：MIT，gofpdi-LICENSE.txt（gopdf的间接依赖，本功能不导入外部PDF）。
- github.com/pkg/errors v0.8.1：BSD-2-Clause，pkg-errors-LICENSE.txt。
- 内嵌霞鹜文楷v1.530：SIL OFL-1.1，见../../internal/novel/fonts/OFL.txt及SOURCE.md。

分发后端程序时应同时附带本目录及字体OFL许可。生成的小说PDF不改变作者正文的权利归属；这些许可不要求小说正文采用开源许可。
